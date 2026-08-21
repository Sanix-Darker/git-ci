CREATE INDEX IF NOT EXISTS idx_audit_events_created_at
    ON audit_events (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_audit_events_action_created_at
    ON audit_events (action, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_audit_events_actor_created_at
    ON audit_events (actor, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_audit_events_resource_created_at
    ON audit_events (resource_type, created_at DESC, id DESC);
