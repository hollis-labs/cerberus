// Package scheduling is Cerberus's inactive, programmatic scheduled-job core.
// The shared scheduler owns recurrence, claims, concurrency and fire history.
// This package owns effect admission and never starts an engine on construction.
package scheduling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/hollis-labs/libs/util/scheduler"
)

const jobType = "cerberus.job.v1"

type TargetKind string

const (
	ResourceStart  TargetKind = "resource_start"
	ResourceDeploy TargetKind = "resource_deploy"
	PipelineRun    TargetKind = "pipeline_run"
)

// Target names a v2 resource or pipeline, never a command or arbitrary host.
type Target struct {
	Kind TargetKind `json:"kind"`
	ID   string     `json:"id"`
}

type Timing struct {
	Cron     string        `json:"cron,omitempty"`
	Interval time.Duration `json:"interval,omitempty"`
	At       time.Time     `json:"at,omitempty"`
	Location string        `json:"location,omitempty"`
}

// EnvReference carries names only. Resolution and delivery belong to 0117;
// this core refuses to execute a job with references rather than ignoring them.
type EnvReference struct {
	Env string `json:"env"`
	Ref string `json:"ref"`
}

// Job contains no authority, acknowledgment, credential values or caller claims.
// Its content hash, including server generation, fences each durable revision.
type Job struct {
	Incarnation    uint64                  `json:"incarnation,omitempty"`
	Generation     uint64                  `json:"generation,omitempty"`
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	OwnerApp       string                  `json:"owner_app"`
	Timing         Timing                  `json:"timing"`
	Target         Target                  `json:"target"`
	EnvRefs        []EnvReference          `json:"env_refs,omitempty"`
	Enabled        bool                    `json:"enabled"`
	Timeout        time.Duration           `json:"timeout"`
	Overlap        scheduler.OverlapPolicy `json:"overlap,omitempty"`
	Misfire        scheduler.MisfirePolicy `json:"misfire,omitempty"`
	MisfireGrace   time.Duration           `json:"misfire_grace,omitempty"`
	MaxCatchUp     int                     `json:"max_catch_up,omitempty"`
	MaxQueuedFires int                     `json:"max_queued_fires,omitempty"`
	KeepLastN      int                     `json:"keep_last_n,omitempty"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (j Job) Key() string { return j.OwnerApp + "/" + j.ID }

func (j Job) Revision() (string, error) {
	data, err := json.Marshal(j)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (j Job) schedule(now time.Time, maxTimeout time.Duration) (scheduler.Schedule, error) {
	if !namePattern.MatchString(j.ID) || !namePattern.MatchString(j.OwnerApp) || j.Name == "" || len(j.Name) > 256 || !namePattern.MatchString(j.Target.ID) {
		return scheduler.Schedule{}, errors.New("invalid job or target name")
	}
	switch j.Target.Kind {
	case ResourceStart, ResourceDeploy, PipelineRun:
	default:
		return scheduler.Schedule{}, errors.New("unsupported scheduled target")
	}
	if j.Timeout <= 0 || j.Timeout > maxTimeout {
		return scheduler.Schedule{}, errors.New("job timeout must be positive and within the engine deadline")
	}
	seen := map[string]bool{}
	for _, ref := range j.EnvRefs {
		if !envPattern.MatchString(ref.Env) || !namePattern.MatchString(ref.Ref) || seen[ref.Env] {
			return scheduler.Schedule{}, errors.New("environment references must be unique names, never values")
		}
		seen[ref.Env] = true
	}
	n := 0
	if j.Timing.Cron != "" {
		n++
	}
	if j.Timing.Interval != 0 {
		n++
	}
	if !j.Timing.At.IsZero() {
		n++
	}
	if n != 1 {
		return scheduler.Schedule{}, errors.New("choose exactly one of cron, interval or one-off time")
	}
	location := j.Timing.Location
	if location == "" {
		location = "UTC"
	}
	s := scheduler.Schedule{ID: j.Key(), CronExpr: j.Timing.Cron, Interval: j.Timing.Interval, Location: location, Enabled: j.Enabled,
		JobType: jobType, Retry: scheduler.RetryPolicy{MaxAttempts: 1}, Overlap: j.Overlap, Misfire: j.Misfire,
		MisfireGrace: j.MisfireGrace, MaxCatchUp: j.MaxCatchUp, MaxQueuedFires: j.MaxQueuedFires, KeepLastN: j.KeepLastN}
	if err := scheduler.ValidateSchedule(s); err != nil {
		return s, err
	}
	if err := scheduler.ValidatePolicies(s); err != nil {
		return s, err
	}
	var err error
	if scheduler.IsOneTime(s) {
		s.NextRun = j.Timing.At.UTC()
	} else {
		s.NextRun, err = scheduler.NextRunForSchedule(s, now)
	}
	if err != nil {
		return s, err
	}
	s.Payload, err = json.Marshal(j)
	return s, err
}
