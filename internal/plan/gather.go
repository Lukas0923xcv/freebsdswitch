package plan

import (
	"context"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

// Gather collects the live state needed for planning.
func Gather(ctx context.Context, be backend.Backend, desired, prev *config.Config) (Input, error) {
	in := Input{Desired: desired, Prev: prev}
	snap, err := be.Snapshot(ctx)
	if err != nil {
		return in, err
	}
	in.Snap = snap
	if i := snap.Get(desired.Bridge.Name); i != nil && i.Bridge != nil {
		macs, err := be.MACTable(ctx, desired.Bridge.Name)
		if err != nil {
			return in, err
		}
		in.MACs = macs
	}
	if in.Routes, err = be.Routes(ctx); err != nil {
		return in, err
	}
	if in.Sysctls, err = be.Sysctl(ctx, backend.SysctlsOfInterest...); err != nil {
		return in, err
	}
	return in, nil
}

// Apply executes the ops in order and stops at the first error. It
// returns the number of ops executed successfully.
func Apply(ctx context.Context, be backend.Backend, ops []backend.Op, logf func(string, ...any)) (int, error) {
	for i, o := range ops {
		if logf != nil {
			logf("exec: %s", o.String())
		}
		if _, err := backend.Execute(ctx, be, o); err != nil {
			return i, err
		}
	}
	return len(ops), nil
}
