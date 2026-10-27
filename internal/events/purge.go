package events

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5"
)

// The privacy policy's promises, kept as the backend's purge events arrive (TL-665, TL-678):
//   - ticket.purged: all of the ticket's activity goes, but its deletion trail (who deleted, restored
//     and purged it: ids only) stays for the org log;
//   - org.purged: everything the organization has here goes, that event included.
// Both are remembered in purged, so anything for them arriving later is dropped instead of kept.

// purgedOf is what's already purged among the batch's organizations: key org for a whole organization,
// org + "\x00" + ticket for one ticket.
func purgedOf(ctx context.Context, tx pgx.Tx, evs []Event) (map[string]bool, error) {
	orgs := map[string]bool{}
	for _, e := range evs {
		orgs[e.OrgID] = true
	}
	list := make([]string, 0, len(orgs))
	for o := range orgs {
		list = append(list, o)
	}
	rows, err := tx.Query(ctx, `SELECT org_id, ticket_id FROM purged WHERE org_id = ANY($1)`, list)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
		var org, ticket string
		if err := r.Scan(&org, &ticket); err != nil {
			return "", err
		}
		if ticket == "" {
			return org, nil
		}
		return org + "\x00" + ticket, nil
	})
	gone := map[string]bool{}
	for _, k := range keys {
		gone[k] = true
	}
	return gone, err
}

// trail is what's kept of a purged ticket, and the only ticket activity the org log shows unless asked.
var trail = map[string]bool{"ticket.deleted": true, "ticket.restored": true, "ticket.purged": true}

const trailSQL = `('ticket.deleted', 'ticket.restored', 'ticket.purged')`

// aboutTicket: activity on one ticket (a stage, an edit, a comment, a file…), not its deletion trail.
func aboutTicket(e Event) bool {
	return e.TargetType != nil && *e.TargetType == "ticket" && e.TargetID != nil && !trail[e.Action]
}

// late: an event for something already purged.
func late(e Event, gone map[string]bool) bool {
	if gone[e.OrgID] {
		return true
	}
	return aboutTicket(e) && gone[e.OrgID+"\x00"+*e.TargetID]
}

// purge carries out the batch's purge events, in its transaction (stored first, so a stage sent in the
// same batch as its ticket's purge goes too).
func purge(ctx context.Context, tx pgx.Tx, evs []Event) error {
	marked := false
	for _, e := range evs {
		var del, mark string
		var args []any
		switch {
		case e.Action == "ticket.purged" && e.TargetID != nil:
			del = `DELETE FROM events WHERE org_id = $1 AND target_type = 'ticket' AND target_id = $2 AND action NOT IN ` + trailSQL
			mark = `INSERT INTO purged (org_id, ticket_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`
			args = []any{e.OrgID, *e.TargetID}
		case e.Action == "org.purged":
			del = `DELETE FROM events WHERE org_id = $1`
			// the org's own mark stands for all of it: its tickets' marks are no longer needed
			mark = `WITH tickets AS (DELETE FROM purged WHERE org_id = $1 AND ticket_id <> '')
				INSERT INTO purged (org_id) VALUES ($1) ON CONFLICT DO NOTHING`
			args = []any{e.OrgID}
		default:
			continue
		}
		if !marked { // the append-only trigger lets this transaction's deletes through
			if _, err := tx.Exec(ctx, `SET LOCAL auditor.purging = 'on'`); err != nil {
				return err
			}
			marked = true
		}
		tag, err := tx.Exec(ctx, del, args...)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, mark, args...); err != nil {
			return err
		}
		log.Printf("auditor: %s %s: deleted %d event(s)", e.Action, e.OrgID, tag.RowsAffected())
	}
	return nil
}
