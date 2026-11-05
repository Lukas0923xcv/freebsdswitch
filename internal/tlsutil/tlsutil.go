// Package tlsutil manages the web UI certificate: a self-signed
// certificate is generated on first start; an uploaded certificate
// replaces it without restart.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

type Manager struct {
	dir  string
	mu   sync.RWMutex
	cert *tls.Certificate
	info Info
}

type Info struct {
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	DNSNames    []string  `json:"dns_names"`
	IPs         []string  `json:"ips"`
	SelfSigned  bool      `json:"self_signed"`
	Fingerprint string    `json:"sha256_fingerprint"`
}

func (m *Manager) certPath() string { return filepath.Join(m.dir, "cert.pem") }
func (m *Manager) keyPath() string  { return filepath.Join(m.dir, "key.pem") }

// Open loads the certificate in dir, generating a self-signed one if
// missing.
func Open(dir, hostname string) (*Manager, error) {
	m := &Manager{dir: dir}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(m.certPath()); os.IsNotExist(err) {
		c, k, err := SelfSigned(hostname)
		if err != nil {
			return nil, err
		}
		if err := m.write(c, k); err != nil {
			return nil, err
		}
	}
	c, err := os.ReadFile(m.certPath())
	if err != nil {
		return nil, err
	}
	k, err := os.ReadFile(m.keyPath())
	if err != nil {
		return nil, err
	}
	return m, m.load(c, k)
}

func (m *Manager) write(cert, key []byte) error {
	if err := config.WriteFileAtomic(m.keyPath(), key, 0o600); err != nil {
		return err
	}
	return config.WriteFileAtomic(m.certPath(), cert, 0o644)
}

func (m *Manager) load(certPEM, keyPEM []byte) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("invalid certificate/key: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	pair.Leaf = leaf
	sum := sha256.Sum256(leaf.Raw)
	info := Info{Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(), NotBefore: leaf.NotBefore,
		NotAfter: leaf.NotAfter, DNSNames: leaf.DNSNames, SelfSigned: leaf.Issuer.String() == leaf.Subject.String(),
		Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:]))}
	for _, ip := range leaf.IPAddresses {
		info.IPs = append(info.IPs, ip.String())
	}
	m.mu.Lock()
	m.cert = &pair
	m.info = info
	m.mu.Unlock()
	return nil
}

// Install validates and activates an uploaded PEM certificate (chain) and key.
func (m *Manager) Install(certPEM, keyPEM []byte) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("certificate and key do not match or are invalid: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if time.Now().After(leaf.NotAfter) {
		return errors.New("certificate has expired")
	}
	if err := m.write(certPEM, keyPEM); err != nil {
		return err
	}
	return m.load(certPEM, keyPEM)
}

// Regenerate replaces the certificate with a new self-signed one.
func (m *Manager) Regenerate(hostname string) error {
	c, k, err := SelfSigned(hostname)
	if err != nil {
		return err
	}
	if err := m.write(c, k); err != nil {
		return err
	}
	return m.load(c, k)
}

func (m *Manager) Info() Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.info
}

// TLSConfig returns a server config with hot-swappable certificate.
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			m.mu.RLock()
			defer m.mu.RUnlock()
			return m.cert, nil
		},
	}
}

// SelfSigned creates an ECDSA P-256 certificate valid for 5 years.
func SelfSigned(hostname string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if hostname == "" {
		hostname = "bsdswitch"
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostname, Organization: []string{"bsdswitch self-signed"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}
