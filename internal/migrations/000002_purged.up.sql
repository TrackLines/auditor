-- What's been purged (TL-665): an organization (ticket_id '') or one ticket. Events for them that arrive
-- after their purge (a late relay batch, a resend) are dropped, not kept. Only ids: nothing else is left.
CREATE TABLE purged (
    org_id    TEXT NOT NULL,
    ticket_id TEXT NOT NULL DEFAULT '',
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, ticket_id)
);
