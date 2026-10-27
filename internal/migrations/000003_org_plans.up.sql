-- Each organization's plan as the backend last reported it (org.plan_changed, TL-676), for retention: Pro's
-- org log is kept a year, Enterprise's for as long as the organization exists. An organization the auditor
-- has no plan for keeps everything: deleting is the one mistake that can't be undone.
CREATE TABLE org_plans (
    org_id     TEXT PRIMARY KEY,
    plan       TEXT NOT NULL CHECK (plan IN ('free', 'pro', 'enterprise')),
    changed_at TIMESTAMPTZ NOT NULL -- when the change happened, so a late event can't undo a newer one
);
