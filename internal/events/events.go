// Package events stores what the backend sends (TL-663): POST /internal/events takes a batch, keeps the
// good events (a resend of one already kept is fine) and says which it took, so the backend deletes
// exactly those from its outbox and tries the rest again.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxBatch and MaxData bound one request: the backend's relay sends up to MaxBatch at a time.
const (
	MaxBatch = 500
	MaxData  = 16 << 10
)

// Event is one thing that happened, as the backend records it.
type Event struct {
	ID         string          `json:"id"`
	OrgID      string          `json:"org_id"`
	At         time.Time       `json:"at"`
	Actor      *string         `json:"actor"`      // user id, agent or service key name; nil: the system
	ActorKind  string          `json:"actor_kind"` // person, agent, service or system
	Action     string          `json:"action"`     // e.g. ticket.stage, api_key.created
	TargetType *string         `json:"target_type"`
	TargetID   *string         `json:"target_id"`
	ProjectID  *string         `json:"project_id"`
	Data       json.RawMessage `json:"data"` // details: never secrets or ticket content
}

// Rejected is an event that wasn't kept, and why; sending it again won't help.
type Rejected struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type Result struct {
	Accepted []string   `json:"accepted"`
	Rejected []Rejected `json:"rejected"`
}

var (
	action = regexp.MustCompile(`^[a-z_]+\.[a-z_]+$`)
	kinds  = map[string]bool{"person": true, "agent": true, "service": true, "system": true}
)

// problem says what's wrong with an event, or "" when it can be kept. It also fills in empty data.
func problem(e *Event) string {
	switch {
	case uuid.Validate(e.ID) != nil:
		return "id must be a UUID"
	case e.OrgID == "":
		return "org_id is required"
	case e.At.IsZero():
		return "at is required"
	case !kinds[e.ActorKind]:
		return "actor_kind must be person, agent, service or system"
	case !action.MatchString(e.Action):
		return "action must look like thing.happened"
	case e.ProjectID != nil && uuid.Validate(*e.ProjectID) != nil:
		return "project_id must be a UUID"
	case len(e.Data) > MaxData:
		return "data is over 16 KB"
	}
	if d := bytes.TrimSpace(e.Data); len(d) == 0 || bytes.Equal(d, []byte("null")) {
		e.Data = json.RawMessage(`{}`)
	} else if d[0] != '{' || !json.Valid(d) {
		return "data must be a JSON object"
	}
	return ""
}

type Store struct{ DB *pgxpool.Pool }

// Ingest is POST /internal/events: {"events": [...]} → 200 {"accepted": [ids], "rejected": [{id, reason}]}.
func (s Store) Ingest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Events []Event `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&in); err != nil {
		http.Error(w, "body must be {\"events\": [...]}", http.StatusBadRequest)
		return
	}
	if len(in.Events) > MaxBatch {
		http.Error(w, "up to 500 events a request", http.StatusBadRequest)
		return
	}
	out, err := s.Store(r.Context(), in.Events)
	if err != nil {
		if r.Context().Err() == nil {
			logs.Errorf("events: store: %v", err)
			http.Error(w, "couldn't store the events", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// Store keeps the good events in one transaction. An id already kept for the same organization counts as
// accepted (a resend); one kept for another organization is rejected, since the backend reused an id.
func (s Store) Store(ctx context.Context, batch []Event) (Result, error) {
	out := Result{Accepted: []string{}, Rejected: []Rejected{}}
	var good []Event
	seen := map[string]bool{}
	for i := range batch {
		e := &batch[i]
		if why := problem(e); why != "" {
			out.Rejected = append(out.Rejected, Rejected{e.ID, why})
		} else if seen[e.ID] {
			out.Rejected = append(out.Rejected, Rejected{e.ID, "id appears twice in the batch"})
		} else {
			seen[e.ID] = true
			good = append(good, *e)
		}
	}
	if len(good) == 0 {
		return out, nil
	}
	rows, _ := json.Marshal(good)
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO events (id, org_id, at, actor, actor_kind, action, target_type, target_id, project_id, data)
			SELECT id, org_id, at, actor, actor_kind, action, target_type, target_id, project_id, data
			FROM jsonb_to_recordset($1::jsonb) AS e(id uuid, org_id text, at timestamptz, actor text, actor_kind text, action text,
				target_type text, target_id text, project_id uuid, data jsonb)
			ON CONFLICT (id) DO NOTHING`, string(rows)); err != nil {
			return err
		}
		// which org each id now belongs to: ours (new or a resend) or another's (a reused id)
		ids := make([]string, len(good))
		for i, e := range good {
			ids[i] = e.ID
		}
		found, err := tx.Query(ctx, `SELECT id::text, org_id FROM events WHERE id = ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
		orgs, err := pgx.CollectRows(found, func(r pgx.CollectableRow) ([2]string, error) {
			var p [2]string
			return p, r.Scan(&p[0], &p[1])
		})
		if err != nil {
			return err
		}
		owner := map[string]string{}
		for _, p := range orgs {
			owner[p[0]] = p[1]
		}
		for _, e := range good {
			if owner[uuid.MustParse(e.ID).String()] == e.OrgID {
				out.Accepted = append(out.Accepted, e.ID)
			} else {
				out.Rejected = append(out.Rejected, Rejected{e.ID, "id already used for another organization"})
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return out, nil
}
