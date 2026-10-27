package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func eventFor(org, action string, ticket *string) Event {
	tt := str("ticket")
	if ticket == nil {
		tt = nil
	}
	return Event{ID: uuid.NewString(), OrgID: org, At: time.Now().UTC(), ActorKind: "system", Action: action, TargetType: tt, TargetID: ticket, Data: json.RawMessage(`{}`)}
}

func countWhere(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db(t).QueryRow(context.Background(), `SELECT count(*) FROM events WHERE `+sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ticket.purged: only that ticket's stages go; its org log entries and other tickets stay, and stages
// arriving afterwards (a late batch, a resend) are taken but not kept
func TestTicketPurge(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: db(t)}
	org := "org_tp_" + uuid.NewString()[:8]
	gone, other := uuid.NewString(), uuid.NewString()
	first := []Event{eventFor(org, "ticket.stage", &gone), eventFor(org, "ticket.stage", &gone), eventFor(org, "ticket.stage", &other), eventFor(org, "ticket.deleted", &gone)}
	if _, err := s.Store(ctx, first); err != nil {
		t.Fatal(err)
	}
	// a stage in the same batch as the purge goes too
	sameBatch := eventFor(org, "ticket.stage", &gone)
	res, err := s.Store(ctx, []Event{sameBatch, eventFor(org, "ticket.purged", &gone)})
	if err != nil || len(res.Accepted) != 2 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	if n := countWhere(t, `org_id = $1 AND action = 'ticket.stage' AND target_id = $2`, org, gone); n != 0 {
		t.Fatalf("%d stages left", n)
	}
	if countWhere(t, `org_id = $1 AND target_id = $2`, org, other) != 1 || countWhere(t, `org_id = $1 AND action = 'ticket.deleted'`, org) != 1 ||
		countWhere(t, `org_id = $1 AND action = 'ticket.purged'`, org) != 1 {
		t.Fatal("deleted more than the ticket's stages")
	}
	// late: accepted (so the backend forgets it) but not kept; a resend of an old stage likewise
	lateOne := eventFor(org, "ticket.stage", &gone)
	res, err = s.Store(ctx, []Event{lateOne, first[0]})
	if err != nil || len(res.Accepted) != 2 || len(res.Rejected) != 0 || countWhere(t, `target_id = $1 AND action = 'ticket.stage'`, gone) != 0 {
		t.Fatalf("late: %+v %v", res, err)
	}
	// other tickets carry on as normal
	if _, err := s.Store(ctx, []Event{eventFor(org, "ticket.stage", &other)}); err != nil || countWhere(t, `target_id = $1`, other) != 2 {
		t.Fatalf("other ticket: %v", err)
	}
}

// org.purged: everything of the organization goes, that event included, and nobody else's; anything for
// it arriving afterwards is taken but not kept
func TestOrgPurge(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: db(t)}
	org, keep := "org_op_"+uuid.NewString()[:8], "org_op_keep_"+uuid.NewString()[:8]
	ticket := uuid.NewString()
	if _, err := s.Store(ctx, []Event{eventFor(org, "ticket.stage", &ticket), eventFor(org, "api_key.created", nil), eventFor(org, "ticket.purged", &ticket),
		eventFor(keep, "api_key.created", nil)}); err != nil {
		t.Fatal(err)
	}
	purgeEv := eventFor(org, "org.purged", nil)
	if res, err := s.Store(ctx, []Event{purgeEv}); err != nil || len(res.Accepted) != 1 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	if n := countWhere(t, `org_id = $1`, org); n != 0 {
		t.Fatalf("%d of the org's events left", n)
	}
	if countWhere(t, `org_id = $1`, keep) != 1 {
		t.Fatal("another org's events went")
	}
	// the org's own mark stands for its tickets
	var marks int
	_ = db(t).QueryRow(ctx, `SELECT count(*) FROM purged WHERE org_id = $1`, org).Scan(&marks)
	if marks != 1 {
		t.Fatalf("%d marks", marks)
	}
	late := []Event{eventFor(org, "member.removed", nil), purgeEv}
	if res, err := s.Store(ctx, late); err != nil || len(res.Accepted) != 2 || countWhere(t, `org_id = $1`, org) != 0 {
		t.Fatalf("late: %+v %v", res, err)
	}
	// the trigger still refuses any delete that isn't a purge
	if _, err := db(t).Exec(ctx, `DELETE FROM events WHERE org_id = $1`, keep); err == nil {
		t.Fatal("a plain delete went through")
	}
}
