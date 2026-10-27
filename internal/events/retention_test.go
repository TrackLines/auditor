package events

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TL-676: the org log is kept a year on Pro (and Free), for as long as the organization exists on
// Enterprise; a ticket's own activity isn't aged out; an organization with no known plan keeps everything
func TestRetention(t *testing.T) {
	ctx := context.Background()
	s, r := Store{DB: db(t)}, Retention{DB: db(t)}
	now := time.Date(2027, 11, 1, 0, 0, 0, 0, time.UTC)
	old, recent := now.AddDate(-1, 0, -1), now.AddDate(0, -6, 0)
	pro, ent, unknown := "org_rp_"+uuid.NewString()[:8], "org_re_"+uuid.NewString()[:8], "org_ru_"+uuid.NewString()[:8]
	ticket := uuid.NewString()
	at := func(org, action string, when time.Time, data string, tk *string) Event {
		e := eventFor(org, action, tk)
		e.At, e.Data = when, json.RawMessage(data)
		return e
	}
	plan := func(org, p string, when time.Time) Event {
		return at(org, "org.plan_changed", when, fmt.Sprintf(`{"plan":%q}`, p), nil)
	}
	batch := []Event{
		plan(pro, "pro", old.AddDate(0, -1, 0)), plan(ent, "enterprise", old.AddDate(0, -1, 0)),
		at(pro, "api_key.created", old, `{}`, nil), at(pro, "api_key.revoked", recent, `{}`, nil),
		at(pro, "ticket.stage", old, `{"stage":"backlog","outcome":"entered"}`, &ticket), // the ticket's own: not the org log
		at(pro, "ticket.deleted", old, `{}`, &ticket),                                    // its deletion trail is the org log
		at(ent, "api_key.created", old, `{}`, nil),
		at(unknown, "api_key.created", old, `{}`, nil),
	}
	if _, err := s.Store(ctx, batch); err != nil {
		t.Fatal(err)
	}
	actions := func(org string) string {
		rows, _ := s.DB.Query(ctx, `SELECT action FROM events WHERE org_id = $1 ORDER BY at, seq`, org)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			out = append(out, a)
		}
		return fmt.Sprint(out)
	}
	n, err := r.Sweep(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	// pro: the old plan change, key and deletion trail go; the ticket's stage and the recent key stay
	if got := actions(pro); got != "[ticket.stage api_key.revoked]" || n < 3 {
		t.Fatalf("pro after the sweep (%d deleted): %s", n, got)
	}
	if got := actions(ent); got != "[org.plan_changed api_key.created]" {
		t.Fatalf("enterprise: %s", got)
	}
	if got := actions(unknown); got != "[api_key.created]" {
		t.Fatalf("no known plan: %s", got)
	}

	// a downgrade to Pro: the year applies from the next sweep
	if _, err := s.Store(ctx, []Event{plan(ent, "pro", recent)}); err != nil {
		t.Fatal(err)
	}
	// a late, older plan change can't undo it
	if _, err := s.Store(ctx, []Event{plan(ent, "enterprise", old)}); err != nil {
		t.Fatal(err)
	}
	var p string
	_ = s.DB.QueryRow(ctx, `SELECT plan FROM org_plans WHERE org_id = $1`, ent).Scan(&p)
	if p != "pro" {
		t.Fatalf("plan after a late older change: %s", p)
	}
	if _, err := r.Sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	if got := actions(ent); got != "[org.plan_changed]" { // only the recent downgrade is left
		t.Fatalf("enterprise downgraded: %s", got)
	}
	// nothing else may delete
	if _, err := s.DB.Exec(ctx, `DELETE FROM events WHERE org_id = $1`, ent); err == nil {
		t.Fatal("a plain delete went through")
	}
	// the org purged: its plan goes with it
	if _, err := s.Store(ctx, []Event{at(ent, "org.purged", now, `{}`, nil)}); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM org_plans WHERE org_id = $1`, ent).Scan(&left)
	if left != 0 {
		t.Fatal("a purged org's plan stayed")
	}
}
