package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracklines/auditor/internal/migrations"
	"github.com/tracklines/auditor/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Run(m)) }

var (
	dbOnce sync.Once
	shared *pgxpool.Pool
)

func db(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbOnce.Do(func() {
		p, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
		if err == nil {
			err = migrations.Up(p)
		}
		if err != nil {
			t.Fatal(err)
		}
		shared = p
	})
	return shared
}

func str(s string) *string { return &s }

func event(org string) Event {
	return Event{ID: uuid.NewString(), OrgID: org, At: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), Actor: str("user_1"), ActorKind: "person",
		Action: "ticket.stage", TargetType: str("ticket"), TargetID: str(uuid.NewString()), ProjectID: str(uuid.NewString()), Data: json.RawMessage(`{"stage":"backlog","outcome":"entered"}`)}
}

func count(t *testing.T, org string) int {
	t.Helper()
	var n int
	if err := db(t).QueryRow(context.Background(), `SELECT count(*) FROM events WHERE org_id = $1`, org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// what's wrong with an event, in words the backend's logs can use
func TestProblem(t *testing.T) {
	for want, e := range map[string]Event{
		"":                   event("o"),
		"id must be a UUID":  func() Event { e := event("o"); e.ID = "1"; return e }(),
		"org_id is required": func() Event { e := event("o"); e.OrgID = ""; return e }(),
		"at is required":     func() Event { e := event("o"); e.At = time.Time{}; return e }(),
		"actor_kind must be person, agent, service or system": func() Event { e := event("o"); e.ActorKind = "robot"; return e }(),
		"action must look like thing.happened":                func() Event { e := event("o"); e.Action = "Ticket Moved"; return e }(),
		"project_id must be a UUID":                           func() Event { e := event("o"); e.ProjectID = str("p1"); return e }(),
		"data is over 16 KB": func() Event {
			e := event("o")
			e.Data = json.RawMessage(`{"x":"` + strings.Repeat("a", MaxData) + `"}`)
			return e
		}(),
		"data must be a JSON object": func() Event { e := event("o"); e.Data = json.RawMessage(`[1]`); return e }(),
	} {
		if got := problem(&e); got != want {
			t.Errorf("%q: got %q", want, got)
		}
	}
	e := event("o")
	e.Data = nil
	if problem(&e) != "" || string(e.Data) != "{}" {
		t.Fatalf("no data becomes {}: %s", e.Data)
	}
	e.Actor, e.ActorKind, e.TargetType, e.TargetID, e.ProjectID = nil, "system", nil, nil, nil // a job, about the org itself
	if problem(&e) != "" {
		t.Fatal("system event refused")
	}
}

// a mixed batch: good events kept, bad ones listed with why; sending it all again changes nothing
func TestStoreMixedBatchAndResend(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: db(t)}
	good1, good2, bad := event("org_mix"), event("org_mix"), event("org_mix")
	bad.Action = "nope"
	twice := good1
	res, err := s.Store(ctx, []Event{good1, bad, good2, twice})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.Accepted) != fmt.Sprint([]string{good1.ID, good2.ID}) || len(res.Rejected) != 2 ||
		res.Rejected[0].ID != bad.ID || res.Rejected[1].Reason != "id appears twice in the batch" {
		t.Fatalf("result: %+v", res)
	}
	if n := count(t, "org_mix"); n != 2 {
		t.Fatalf("stored %d", n)
	}
	again, err := s.Store(ctx, []Event{good1, good2})
	if err != nil || len(again.Accepted) != 2 || len(again.Rejected) != 0 || count(t, "org_mix") != 2 {
		t.Fatalf("resend: %+v %v, %d stored", again, err, count(t, "org_mix"))
	}
	// the same id for another organization is a backend bug: refused, the original untouched
	stolen := good1
	stolen.OrgID = "org_other"
	res, _ = s.Store(ctx, []Event{stolen})
	if len(res.Accepted) != 0 || res.Rejected[0].Reason != "id already used for another organization" || count(t, "org_other") != 0 {
		t.Fatalf("reused id: %+v", res)
	}
	// stored as sent, data and all
	var data, actor string
	var at time.Time
	if err := db(t).QueryRow(ctx, `SELECT data::text, actor, at FROM events WHERE id = $1`, good1.ID).Scan(&data, &actor, &at); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, `"stage": "backlog"`) || actor != "user_1" || !at.Equal(good1.At) {
		t.Fatalf("stored %s %s %s", data, actor, at)
	}
	// nothing good: nothing to do
	if res, err := s.Store(ctx, []Event{bad}); err != nil || len(res.Accepted) != 0 || len(res.Rejected) != 1 {
		t.Fatalf("all bad: %+v %v", res, err)
	}
}

// append-only: no edits, no deletes or truncates, except the purge's own deletes
func TestAppendOnly(t *testing.T) {
	ctx := context.Background()
	p := db(t)
	e := event("org_ro")
	if _, err := (Store{DB: p}).Store(ctx, []Event{e}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE events SET actor = 'someone else' WHERE org_id = 'org_ro'`,
		`DELETE FROM events WHERE org_id = 'org_ro'`,
		`TRUNCATE events`,
	} {
		if _, err := p.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if count(t, "org_ro") != 1 {
		t.Fatal("changed")
	}
	// the purge marks its own transaction
	err := pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL auditor.purging = 'on'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM events WHERE org_id = 'org_ro'`)
		return err
	})
	if err != nil || count(t, "org_ro") != 0 {
		t.Fatalf("purge: %v", err)
	}
	// and only that transaction
	if _, err := p.Exec(ctx, `DELETE FROM events`); err == nil {
		t.Fatal("the flag outlived its transaction")
	}
}

func TestIngestHTTP(t *testing.T) {
	s := Store{DB: db(t)}
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Ingest(rec, httptest.NewRequest(http.MethodPost, "/internal/events", strings.NewReader(body)))
		return rec
	}
	e := event("org_http")
	b, _ := json.Marshal(map[string]any{"events": []Event{e}})
	rec := post(string(b))
	var res Result
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || len(res.Accepted) != 1 || res.Accepted[0] != e.ID || res.Rejected == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post(`not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
	big := make([]Event, MaxBatch+1)
	b, _ = json.Marshal(map[string]any{"events": big})
	if rec := post(string(b)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "up to 500") {
		t.Fatalf("too many: %d %s", rec.Code, rec.Body)
	}
}
