package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func get(t *testing.T, h http.HandlerFunc, path string, vals map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range vals {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// a ticket's history reads exactly like the backend's: oldest first by when it happened (backfilled
// stages land before newer ones), only that ticket's ticket.stage events, only in that org
func TestHistoryShape(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: db(t)}
	ticket := uuid.NewString()
	stage := func(at time.Time, data string, actor *string) Event {
		return Event{ID: uuid.NewString(), OrgID: "org_hist", At: at, Actor: actor, ActorKind: "person", Action: "ticket.stage",
			TargetType: str("ticket"), TargetID: &ticket, Data: json.RawMessage(data)}
	}
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	moved := stage(t0.Add(time.Hour), `{"stage":"column","outcome":"entered","stage_order":2,"board_id":"b1","board_name":"Web","column_id":"c2","column_name":"Doing","done":false,"actor":"not this"}`, str("user_2"))
	created := stage(t0, `{"stage":"backlog","outcome":"skipped","stage_order":0}`, str("user_1")) // stored later: a backfill
	resolved := stage(t0.Add(2*time.Hour), `{"stage":"resolved","outcome":"entered","stage_order":1000000,"resolution":"done"}`, nil)
	other := stage(t0, `{"stage":"backlog","outcome":"entered","stage_order":0}`, nil)
	other.OrgID = "org_hist_other"
	notStage := stage(t0, `{}`, nil)
	notStage.Action = "ticket.deleted"
	for _, batch := range [][]Event{{moved}, {created, resolved, other, notStage}} {
		if _, err := s.Store(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	rec := get(t, s.History, "/", map[string]string{"org": "org_hist", "ticket": ticket})
	want := `[{"at":"2026-10-01T09:00:00Z","stage":"backlog","outcome":"skipped","stage_order":0,"done":false,"actor":"user_1"},` +
		`{"at":"2026-10-01T10:00:00Z","stage":"column","outcome":"entered","stage_order":2,"board_id":"b1","board_name":"Web","column_id":"c2","column_name":"Doing","done":false,"actor":"user_2"},` +
		`{"at":"2026-10-01T11:00:00Z","stage":"resolved","outcome":"entered","stage_order":1000000,"done":false,"resolution":"done"}]`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("%d\n got %s\nwant %s", rec.Code, rec.Body, want)
	}
	if rec := get(t, s.History, "/", map[string]string{"org": "org_hist", "ticket": uuid.NewString()}); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("no history: %s", rec.Body)
	}
	if rec := get(t, s.History, "/", map[string]string{"org": "org_somebody_else", "ticket": ticket}); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("another org read it: %s", rec.Body)
	}
}

// the org log: newest first, filters, cursor paging, the plan's since, never another org
func TestOrgLog(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: db(t)}
	org := "org_log"
	project := uuid.NewString()
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var batch []Event
	add := func(day int, action, actor string, extra func(*Event)) {
		e := Event{ID: uuid.NewString(), OrgID: org, At: t0.AddDate(0, 0, day), Actor: &actor, ActorKind: "person", Action: action, Data: json.RawMessage(`{}`)}
		if extra != nil {
			extra(&e)
		}
		batch = append(batch, e)
	}
	add(0, "api_key.created", "user_1", nil)
	add(1, "api_key.revoked", "user_2", nil)
	add(2, "member.removed", "user_1", func(e *Event) { e.TargetType, e.TargetID = str("member"), str("user_9") })
	add(3, "board.created", "user_2", func(e *Event) { e.ProjectID = &project })
	add(4, "ticket.stage", "user_1", func(e *Event) { e.TargetType, e.TargetID = str("ticket"), str(uuid.NewString()) }) // left out unless asked for
	add(5, "org.settings_changed", "user_1", nil)
	elsewhere := batch[0]
	elsewhere.ID, elsewhere.OrgID = uuid.NewString(), "org_log_other"
	if _, err := s.Store(ctx, append(batch, elsewhere)); err != nil {
		t.Fatal(err)
	}
	actions := func(f Filter) []string {
		t.Helper()
		if f.Limit == 0 {
			f.Limit = DefaultLimit
		}
		p, err := s.Page(ctx, org, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range p.Events {
			out = append(out, e.Action)
		}
		return out
	}
	for name, c := range map[string]struct {
		f    Filter
		want string
	}{
		"newest first, no stages": {Filter{}, "[org.settings_changed board.created member.removed api_key.revoked api_key.created]"},
		"action prefix":           {Filter{Action: "api_key."}, "[api_key.revoked api_key.created]"},
		"exact action":            {Filter{Action: "api_key.created"}, "[api_key.created]"},
		"stages when asked":       {Filter{Action: "ticket.stage"}, "[ticket.stage]"},
		"actor":                   {Filter{Actor: "user_2"}, "[board.created api_key.revoked]"},
		"target":                  {Filter{TargetType: "member", TargetID: "user_9"}, "[member.removed]"},
		"project":                 {Filter{ProjectID: project}, "[board.created]"},
		"from and to":             {Filter{From: t0.AddDate(0, 0, 1), To: t0.AddDate(0, 0, 3)}, "[member.removed api_key.revoked]"},
		"since (retention)":       {Filter{Since: t0.AddDate(0, 0, 3)}, "[org.settings_changed board.created]"},
		"since wins over from":    {Filter{From: t0, Since: t0.AddDate(0, 0, 5)}, "[org.settings_changed]"},
	} {
		if got := fmt.Sprint(actions(c.f)); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
	if p, _ := s.Page(ctx, "org_log_other", Filter{Limit: 50}); len(p.Events) != 1 {
		t.Fatalf("org isolation: %+v", p)
	}

	// paging: two at a time, by cursor, to the end
	var seen []string
	cursor := int64(0)
	for pages := 0; ; pages++ {
		p, err := s.Page(ctx, org, Filter{Limit: 2, Cursor: cursor})
		if err != nil || pages > 5 {
			t.Fatalf("%v after %d pages", err, pages)
		}
		for _, e := range p.Events {
			seen = append(seen, e.Action)
		}
		if p.NextCursor == nil {
			break
		}
		fmt.Sscan(*p.NextCursor, &cursor)
	}
	if fmt.Sprint(seen) != "[org.settings_changed board.created member.removed api_key.revoked api_key.created]" {
		t.Fatalf("paged: %v", seen)
	}

	// over HTTP: query parameters, and the ones it can't read
	rec := get(t, s.Log, "/?action=api_key.&limit=1", map[string]string{"org": org})
	var p Page
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusOK || len(p.Events) != 1 || p.Events[0].Action != "api_key.revoked" || p.NextCursor == nil || string(p.Events[0].Data) != "{}" {
		t.Fatalf("http: %d %s", rec.Code, rec.Body)
	}
	for _, q := range []string{"limit=0", "limit=201", "cursor=x", "from=yesterday", "project_id=p1"} {
		if rec := get(t, s.Log, "/?"+q, map[string]string{"org": org}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := get(t, s.Log, "/", map[string]string{"org": "org_nobody"}); strings.TrimSpace(rec.Body.String()) != `{"events":[],"next_cursor":null}` {
		t.Fatalf("empty: %s", rec.Body)
	}
}
