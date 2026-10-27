package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TL-678: a ticket's activity is every event aimed at it, oldest first; purging it keeps only who deleted,
// restored and purged it; anything late for it is dropped; the org log leaves ticket activity out unless asked
func TestTicketActivity(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: db(t)}
	org := "org_ta_" + uuid.NewString()[:8]
	ticket, other := uuid.NewString(), uuid.NewString()
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	on := func(id string, min int, action string) Event {
		e := eventFor(org, action, &id)
		e.At = t0.Add(time.Duration(min) * time.Minute)
		return e
	}
	var batch []Event
	for i, a := range []string{"ticket.stage", "ticket.assigned", "ticket.updated", "comment.added", "attachment.added", "ticket.deleted", "ticket.restored"} {
		batch = append(batch, on(ticket, i, a))
	}
	batch = append(batch, on(other, 0, "comment.added"), eventFor(org, "api_key.created", nil))
	if _, err := s.Store(ctx, batch); err != nil {
		t.Fatal(err)
	}

	activity := func() []string {
		rec := get(t, s.Activity, "/", map[string]string{"org": org, "ticket": ticket})
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var evs []Stored
		_ = json.Unmarshal(rec.Body.Bytes(), &evs)
		var out []string
		for _, e := range evs {
			if e.ID == "" || e.At == "" {
				t.Fatalf("shape: %+v", e)
			}
			out = append(out, e.Action)
		}
		return out
	}
	if got := fmt.Sprint(activity()); got != "[ticket.stage ticket.assigned ticket.updated comment.added attachment.added ticket.deleted ticket.restored]" {
		t.Fatalf("activity: %s", got)
	}

	// the org log: no ticket activity except the deletion trail, unless asked for
	log := func(f Filter) string {
		f.Limit = 50
		p, err := s.Page(ctx, org, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range p.Events {
			out = append(out, e.Action)
		}
		return fmt.Sprint(out)
	}
	if got := log(Filter{}); got != "[api_key.created ticket.restored ticket.deleted]" {
		t.Fatalf("org log: %s", got)
	}
	if got := log(Filter{Action: "comment."}); got != "[comment.added comment.added]" {
		t.Fatalf("asked for comments: %s", got)
	}

	// purged: only its deletion trail is left; the other ticket and the org's own events stay
	if _, err := s.Store(ctx, []Event{on(ticket, 10, "ticket.purged")}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(activity()); got != "[ticket.deleted ticket.restored ticket.purged]" {
		t.Fatalf("after the purge: %s", got)
	}
	if countWhere(t, `org_id = $1 AND target_id = $2`, org, other) != 1 || countWhere(t, `org_id = $1 AND action = 'api_key.created'`, org) != 1 {
		t.Fatal("the purge reached past its ticket")
	}
	// late: any activity is dropped (taken, not kept); a late piece of the deletion trail is kept
	res, err := s.Store(ctx, []Event{on(ticket, 11, "comment.edited"), on(ticket, 12, "ticket.updated"), on(ticket, 13, "ticket.deleted")})
	if err != nil || len(res.Accepted) != 3 {
		t.Fatalf("late: %+v %v", res, err)
	}
	if got := fmt.Sprint(activity()); got != "[ticket.deleted ticket.restored ticket.purged ticket.deleted]" {
		t.Fatalf("late events: %s", got)
	}
}
