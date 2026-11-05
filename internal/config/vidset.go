package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// VIDSet is a sorted set of 802.1Q VLAN IDs. In YAML it is written as a
// compact range string ("10,20,30-39") but a plain list of integers is
// accepted as well.
type VIDSet []int

// ParseVIDSet parses "10,20,30-39", "none", "all" or "".
func ParseVIDSet(s string) (VIDSet, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "none" {
		return VIDSet{}, nil
	}
	if s == "all" {
		s = "1-4094"
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if i := strings.Index(part, "-"); i > 0 {
			lo, hi = part[:i], part[i+1:]
		}
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			return nil, fmt.Errorf("invalid VLAN ID %q", lo)
		}
		b, err := strconv.Atoi(strings.TrimSpace(hi))
		if err != nil {
			return nil, fmt.Errorf("invalid VLAN ID %q", hi)
		}
		if a > b {
			return nil, fmt.Errorf("invalid VLAN range %q", part)
		}
		if a < MinVID || b > MaxVID {
			return nil, fmt.Errorf("VLAN ID out of range 1-4094 in %q", part)
		}
		for v := a; v <= b; v++ {
			seen[v] = true
		}
	}
	out := make(VIDSet, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Ints(out)
	return out, nil
}

// String renders the set in compact range form. Empty set renders "".
func (s VIDSet) String() string {
	n := s.Normalize()
	var b strings.Builder
	for i := 0; i < len(n); {
		j := i
		for j+1 < len(n) && n[j+1] == n[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if j == i {
			fmt.Fprintf(&b, "%d", n[i])
		} else {
			fmt.Fprintf(&b, "%d-%d", n[i], n[j])
		}
		i = j + 1
	}
	return b.String()
}

// Normalize returns a sorted, de-duplicated copy.
func (s VIDSet) Normalize() VIDSet {
	if len(s) == 0 {
		return VIDSet{}
	}
	c := append(VIDSet(nil), s...)
	sort.Ints(c)
	out := c[:1]
	for _, v := range c[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func (s VIDSet) Contains(v int) bool {
	i := sort.SearchInts(s, v)
	return i < len(s) && s[i] == v
}

// Add returns a new set with v added.
func (s VIDSet) Add(v int) VIDSet { return append(append(VIDSet(nil), s...), v).Normalize() }

// Remove returns a new set without v.
func (s VIDSet) Remove(v int) VIDSet {
	out := VIDSet{}
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// Minus returns s \ o.
func (s VIDSet) Minus(o VIDSet) VIDSet {
	out := VIDSet{}
	for _, x := range s {
		if !o.Contains(x) {
			out = append(out, x)
		}
	}
	return out
}

func (s VIDSet) Equal(o VIDSet) bool {
	a, b := s.Normalize(), o.Normalize()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s VIDSet) MarshalYAML() (interface{}, error) { return s.String(), nil }

func (s *VIDSet) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		v, err := ParseVIDSet(n.Value)
		if err != nil {
			return err
		}
		*s = v
		return nil
	case yaml.SequenceNode:
		var parts []string
		for _, c := range n.Content {
			parts = append(parts, c.Value)
		}
		v, err := ParseVIDSet(strings.Join(parts, ","))
		if err != nil {
			return err
		}
		*s = v
		return nil
	}
	return fmt.Errorf("line %d: VLAN list must be a string like \"10,20,30-39\" or a list", n.Line)
}

// JSON: emit as array of ints (easier for the UI), accept either form.
func (s VIDSet) MarshalJSON() ([]byte, error) {
	n := s.Normalize()
	parts := make([]string, len(n))
	for i, v := range n {
		parts[i] = strconv.Itoa(v)
	}
	return []byte("[" + strings.Join(parts, ",") + "]"), nil
}

func (s *VIDSet) UnmarshalJSON(b []byte) error {
	str := strings.TrimSpace(string(b))
	if str == "null" {
		*s = VIDSet{}
		return nil
	}
	if strings.HasPrefix(str, "\"") {
		uq, err := strconv.Unquote(str)
		if err != nil {
			return err
		}
		v, err := ParseVIDSet(uq)
		if err != nil {
			return err
		}
		*s = v
		return nil
	}
	str = strings.Trim(str, "[]")
	v, err := ParseVIDSet(str)
	if err != nil {
		return err
	}
	*s = v
	return nil
}
