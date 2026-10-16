package eventexport

import (
	"context"
	"path/filepath"

	"github.com/hollis-labs/cerberus/internal/scheduling"
)

// SchedulingSink is a scheduling.NotificationSink that writes each failure and
// recovery notification into the scheduling-channel outbox as alert.raised and
// alert.cleared.
//
// Send returns nil only once the envelope is durable. It is idempotent: a
// notification's EventID is the outbox key, so a repeated Send (after an
// ambiguous failure or a restart) appends nothing and returns nil. That is what
// lets Idempotent report true. Misfires and other observations stay local: Send
// accepts them and exports nothing.
//
// The sink only writes the outbox. Constructing it, or calling Send, starts no
// job and authorizes no operation.
type SchedulingSink struct {
	outbox *FileOutbox
}

var _ scheduling.NotificationSink = (*SchedulingSink)(nil)

// OpenSchedulingSink opens (creating if needed) the scheduling-channel outbox
// under outboxDir.
func OpenSchedulingSink(outboxDir string) (*SchedulingSink, error) {
	box, err := OpenFileOutbox(filepath.Join(outboxDir, ChannelScheduling))
	if err != nil {
		return nil, err
	}
	return &SchedulingSink{outbox: box}, nil
}

// Outbox is the scheduling-channel outbox, for a relay to read.
func (s *SchedulingSink) Outbox() Outbox { return s.outbox }

// Idempotent implements scheduling.NotificationSink: a repeated EventID is
// durably deduplicated by the outbox.
func (s *SchedulingSink) Idempotent() bool { return true }

// Send implements scheduling.NotificationSink.
func (s *SchedulingSink) Send(ctx context.Context, n scheduling.Notification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var kind, severity, summary string
	switch n.Kind {
	case "failure":
		kind, severity, summary = "alert.raised", "warning", "a scheduled job failed"
	case "recovery":
		kind, severity, summary = "alert.cleared", "info", "a scheduled job recovered"
	default:
		return nil // misfire and other observations stay local
	}
	refs := map[string]any{"job_key": n.JobKey, "incarnation": n.Incarnation, "episode_id": n.EpisodeID, "fire_id": n.FireID}
	env := alert(kind, RuleScheduleJobFailed, summary, severity, s.outbox.now(), "msg://schedule_job/cerberus/"+n.JobKey,
		[]string{"scheduling", n.Kind}, refs, "schedule-job/"+n.JobKey, ChannelScheduling)
	env.Class.Category = "alert"
	env.Corr = corr(n.EpisodeID)
	// Durable acceptance is the outbox's own key: the notification's event id.
	_, _, err := s.outbox.Append("n/"+n.EventID, SourceRef{Source: "scheduling"}, env)
	return err
}
