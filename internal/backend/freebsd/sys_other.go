//go:build !freebsd

package freebsd

import (
	"context"
	"errors"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
)

type cpuSampler struct{}

var errNotFreeBSD = errors.New("not running on FreeBSD")

func (l *Local) Counters(ctx context.Context) (map[string]backend.Counters, error) {
	return nil, errNotFreeBSD
}

func (l *Local) SysInfo(ctx context.Context) (backend.SysInfo, error) {
	return backend.SysInfo{}, errNotFreeBSD
}
