-- Every event the backend sends (TL-663): who did what to what, in which organization, and when. One table
-- for ticket stages (action ticket.stage) and the org audit log. Append-only: see the trigger below.
CREATE TABLE events (
    id          UUID PRIMARY KEY,                               -- the backend's outbox id: a resend is a no-op
    seq         BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE,     -- the order events were stored in (paging)
    org_id      TEXT NOT NULL,
    at          TIMESTAMPTZ NOT NULL,                           -- when it happened, by the backend's clock
    stored_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor       TEXT,                                           -- user id, agent or service key name; null: the system
    actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('person', 'agent', 'service', 'system')),
    action      TEXT NOT NULL CHECK (action ~ '^[a-z_]+\.[a-z_]+$'),
    target_type TEXT,
    target_id   TEXT,
    project_id  UUID,
    data        JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(data) = 'object') -- details: never secrets or ticket content
);
CREATE INDEX events_org_idx ON events (org_id, seq);                                -- the org log
CREATE INDEX events_target_idx ON events (org_id, target_type, target_id, seq);     -- one ticket's history

-- Append-only: never changed, and deleted only by the auditor's own purge (retention and deletion), which
-- sets auditor.purging for its transaction. A guard against our own mistakes, not against someone who can
-- run SQL on this database: they could set the flag too.
CREATE FUNCTION events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('auditor.purging', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'events are append-only' USING ERRCODE = 'restrict_violation';
END $$;
CREATE TRIGGER events_append_only BEFORE UPDATE OR DELETE ON events
    FOR EACH ROW EXECUTE FUNCTION events_append_only();
CREATE TRIGGER events_no_truncate BEFORE TRUNCATE ON events
    FOR EACH STATEMENT EXECUTE FUNCTION events_append_only();
