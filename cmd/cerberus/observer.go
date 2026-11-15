package main

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// laneObserver feeds the read API from the supervision lane: each route's runtime fields are
// what the lane observes, and in observe mode it only reads.
type laneObserver struct{ lane *supervise.Service }

// Observe reports the lane's observation of r.
func (o laneObserver) Observe(ctx context.Context, r config.ResourceDef) api.Observation {
	st := o.lane.Observe(ctx, &r)
	obs := api.Observation{State: string(st.State), Unit: st.Unit, Detail: st.Detail}
	if rec := st.Systemd; rec != nil {
		obs.Systemd = &api.SystemdDetail{
			LoadState: rec.LoadState, ActiveState: rec.ActiveState, SubState: rec.SubState,
			UnitFileState: rec.UnitFileState, Result: rec.Result,
			PID: rec.PID, ExitStatus: rec.ExecMainStatus, Restarts: rec.Restarts,
			Diagnosis: rec.Diagnosis, Highlights: rec.Highlights,
		}
	}
	return obs
}
