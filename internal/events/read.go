package events

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HistoryEntry is one step in a ticket's lifecycle, exactly as the backend's GET /api/tickets/{id}/history
// has always answered (its tickets.HistoryEntry), so the web app, TUI and MCP don't change (TL-664).
type HistoryEntry struct {
	At         string  `json:"at"`
	Stage      string  `json:"stage"`
	Outcome    string  `json:"outcome"`
	StageOrder int     `json:"stage_order"`
	BoardID    *string `json:"board_id,omitempty"`
	BoardName  *string `json:"board_name,omitempty"`
	ColumnID   *string `json:"column_id,omitempty"`
	ColumnName *string `json:"column_name,omitempty"`
	Done       bool    `json:"done"`
	Resolution *string `json:"resolution,omitempty"`
	Actor      *string `json:"actor,omitempty"`
}

const utc = `'YYYY-MM-DD"T"HH24:MI:SS"Z"'`

// History is GET /internal/orgs/{org}/tickets/{ticket}/history: the ticket's ticket.stage events, oldest
// first. They're ordered by when they happened, then by when they were stored, so history copied across
// later (the backfill) still sits before newer moves. A ticket with none is an empty list: whether the
// ticket exists is the backend's to say.
func (s Store) History(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Query(r.Context(), `SELECT to_char(at AT TIME ZONE 'UTC', `+utc+`), actor, data FROM events
		WHERE org_id = $1 AND target_type = 'ticket' AND target_id = $2 AND action = 'ticket.stage' ORDER BY at, seq`,
		r.PathValue("org"), r.PathValue("ticket"))
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (HistoryEntry, error) {
		var e HistoryEntry
		var at string
		var actor *string
		var data []byte
		if err := row.Scan(&at, &actor, &data); err != nil {
			return e, err
		}
		err := json.Unmarshal(data, &e)
		e.At, e.Actor = at, actor // from the event itself, never its data
		return e, err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if out == nil {
		out = []HistoryEntry{}
	}
	reply(w, out)
}

// Stored is an event as the org log shows it.
type Stored struct {
	ID         string          `json:"id"`
	Seq        int64           `json:"seq"`
	At         string          `json:"at"`
	Actor      *string         `json:"actor"`
	ActorKind  string          `json:"actor_kind"`
	Action     string          `json:"action"`
	TargetType *string         `json:"target_type"`
	TargetID   *string         `json:"target_id"`
	ProjectID  *string         `json:"project_id"`
	Data       json.RawMessage `json:"data"`
}

type Page struct {
	Events     []Stored `json:"events"`
	NextCursor *string  `json:"next_cursor"` // pass as cursor for the next (older) page; null on the last
}

// Filter narrows the org log. Action is exact, or a prefix when it ends in a dot (api_key.). Without an
// action, ticket stages are left out: every move of every ticket would bury what the log is for.
type Filter struct {
	Actor, Action, TargetType, TargetID, ProjectID string
	From, To, Since                                time.Time // From and Since are inclusive, To exclusive
	Cursor                                         int64     // only events older than this seq; 0 = the newest
	Limit                                          int
}

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Log is GET /internal/orgs/{org}/events?actor=&action=&target_type=&target_id=&project_id=&from=&to=&since=&cursor=&limit=,
// newest first. since is the backend's: the plan's retention, so expired events never show, purged or not.
func (s Store) Log(w http.ResponseWriter, r *http.Request) {
	f, why := filterOf(r)
	if why != "" {
		http.Error(w, why, http.StatusBadRequest)
		return
	}
	page, err := s.Page(r.Context(), r.PathValue("org"), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	reply(w, page)
}

func filterOf(r *http.Request) (Filter, string) {
	q := r.URL.Query()
	f := Filter{Actor: q.Get("actor"), Action: q.Get("action"), TargetType: q.Get("target_type"), TargetID: q.Get("target_id"),
		ProjectID: q.Get("project_id"), Limit: DefaultLimit}
	if f.ProjectID != "" && uuid.Validate(f.ProjectID) != nil {
		return f, "project_id must be a UUID"
	}
	for name, t := range map[string]*time.Time{"from": &f.From, "to": &f.To, "since": &f.Since} {
		if v := q.Get(name); v != "" {
			parsed, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return f, name + " must be an RFC 3339 time"
			}
			*t = parsed
		}
	}
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return f, "cursor must be the next_cursor of a page"
		}
		f.Cursor = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxLimit {
			return f, "limit must be 1 to 200"
		}
		f.Limit = n
	}
	return f, ""
}

// Page is one page of org's log, newest first.
func (s Store) Page(ctx context.Context, org string, f Filter) (Page, error) {
	where := []string{"org_id = $1"}
	args := []any{org}
	add := func(sql string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(sql, "?", "$"+strconv.Itoa(len(args))))
	}
	switch {
	case f.Action == "":
		where = append(where, "action <> 'ticket.stage'")
	case strings.HasSuffix(f.Action, "."):
		add("starts_with(action, ?)", f.Action)
	default:
		add("action = ?", f.Action)
	}
	for sql, v := range map[string]string{"actor = ?": f.Actor, "target_type = ?": f.TargetType, "target_id = ?": f.TargetID, "project_id = ?::uuid": f.ProjectID} {
		if v != "" {
			add(sql, v)
		}
	}
	for _, c := range []struct {
		sql string
		t   time.Time
	}{{"at >= ?", f.From}, {"at < ?", f.To}, {"at >= ?", f.Since}} {
		if !c.t.IsZero() {
			add(c.sql, c.t)
		}
	}
	if f.Cursor > 0 {
		add("seq < ?", f.Cursor)
	}
	args = append(args, f.Limit+1) // one more, to know there's a next page
	rows, err := s.DB.Query(ctx, `SELECT id::text, seq, to_char(at AT TIME ZONE 'UTC', `+utc+`), actor, actor_kind, action,
			target_type, target_id, project_id::text, data
		FROM events WHERE `+strings.Join(where, " AND ")+` ORDER BY seq DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return Page{}, err
	}
	evs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Stored, error) {
		var e Stored
		return e, row.Scan(&e.ID, &e.Seq, &e.At, &e.Actor, &e.ActorKind, &e.Action, &e.TargetType, &e.TargetID, &e.ProjectID, &e.Data)
	})
	if err != nil {
		return Page{}, err
	}
	out := Page{Events: evs}
	if out.Events == nil {
		out.Events = []Stored{}
	}
	if len(out.Events) > f.Limit {
		out.Events = out.Events[:f.Limit]
		next := strconv.FormatInt(out.Events[f.Limit-1].Seq, 10)
		out.NextCursor = &next
	}
	return out, nil
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return // the backend gave up: nobody to answer
	}
	logs.Errorf("events: read: %v", err)
	http.Error(w, "couldn't read the events", http.StatusInternalServerError)
}
