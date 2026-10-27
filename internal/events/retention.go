package events

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KeepFor is how long the org log is kept on any plan but Enterprise (TL-676): the privacy policy's year.
const KeepFor = "1 year"

// plans records the batch's plan changes (org.plan_changed: data.plan), the newest per organization winning,
// so an event that arrives late can't undo a newer one.
func plans(ctx context.Context, tx pgx.Tx, evs []Event) error {
	for _, e := range evs {
		if e.Action != "org.plan_changed" {
			continue
		}
		var d struct {
			Plan string `json:"plan"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil || (d.Plan != "free" && d.Plan != "pro" && d.Plan != "enterprise") {
			continue // nothing to go on: keep what's known
		}
		if _, err := tx.Exec(ctx, `INSERT INTO org_plans (org_id, plan, changed_at) VALUES ($1, $2, $3)
			ON CONFLICT (org_id) DO UPDATE SET plan = EXCLUDED.plan, changed_at = EXCLUDED.changed_at
			WHERE org_plans.changed_at <= EXCLUDED.changed_at`, e.OrgID, d.Plan, e.At); err != nil {
			return err
		}
	}
	return nil
}

// Retention ages out the org log (TL-676): on Pro (and Free, should an org drop to it) entries over a year
// old go; on Enterprise they stay until the organization is deleted. A ticket's own activity isn't the org
// log: it lives as long as the ticket (its purge deletes it). An organization with no known plan keeps everything.
type Retention struct{ DB *pgxpool.Pool }

// Run sweeps once soon after starting, then every interval, until ctx ends.
func (r Retention) Run(ctx context.Context, every time.Duration) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n, err := r.Sweep(ctx, time.Now()); err != nil {
			if ctx.Err() == nil {
				logs.Errorf("auditor: retention: %v", err)
			}
		} else {
			log.Printf("auditor: retention: deleted %d org log event(s) older than %s", n, KeepFor)
		}
		t.Reset(every)
	}
}

// Sweep deletes the org log entries past their plan's retention at now, and says how many.
func (r Retention) Sweep(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL auditor.purging = 'on'`); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM events e USING org_plans p
			WHERE p.org_id = e.org_id AND p.plan <> 'enterprise'
			AND e.at < $1::timestamptz - $2::interval
			AND (e.target_type IS DISTINCT FROM 'ticket' OR e.action IN `+trailSQL+`)`, now, KeepFor)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}
