-- Durable configuration state. The initial schema included lightweight
-- placeholders for secrets and schedules; extend those tables in place so
-- existing installations retain their configuration records.

ALTER TABLE secrets ADD COLUMN encryption_algorithm TEXT NOT NULL DEFAULT 'AES-256-GCM'
    CHECK (length(trim(encryption_algorithm)) > 0);
ALTER TABLE secrets ADD COLUMN nonce BLOB;
ALTER TABLE secrets ADD COLUMN ciphertext BLOB;

CREATE TRIGGER IF NOT EXISTS configuration_secrets_envelope_pair_insert
BEFORE INSERT ON secrets
FOR EACH ROW WHEN (NEW.nonce IS NULL) != (NEW.ciphertext IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'secret nonce and ciphertext must be supplied together');
END;

CREATE TRIGGER IF NOT EXISTS configuration_secrets_envelope_pair_update
BEFORE UPDATE OF nonce, ciphertext ON secrets
FOR EACH ROW WHEN (NEW.nonce IS NULL) != (NEW.ciphertext IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'secret nonce and ciphertext must be supplied together');
END;

ALTER TABLE schedules ADD COLUMN workflow_id TEXT REFERENCES workflows(id) ON DELETE CASCADE;
ALTER TABLE schedules ADD COLUMN ref TEXT;
ALTER TABLE schedules ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC'
    CHECK (length(trim(timezone)) > 0);

CREATE INDEX IF NOT EXISTS idx_schedules_project_next
    ON schedules (project_id, active, next_run_at ASC, id ASC);

CREATE TABLE IF NOT EXISTS schedule_claims (
    schedule_id TEXT PRIMARY KEY,
    due_at INTEGER NOT NULL,
    claimed_at INTEGER NOT NULL,
    FOREIGN KEY (schedule_id) REFERENCES schedules(id) ON DELETE CASCADE
);

CREATE TRIGGER IF NOT EXISTS configuration_schedules_workflow_project_insert
BEFORE INSERT ON schedules
FOR EACH ROW WHEN NEW.workflow_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM workflows WHERE id = NEW.workflow_id AND project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'schedule workflow must belong to project');
END;

CREATE TRIGGER IF NOT EXISTS configuration_schedules_workflow_project_update
BEFORE UPDATE OF project_id, workflow_id ON schedules
FOR EACH ROW WHEN NEW.workflow_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM workflows WHERE id = NEW.workflow_id AND project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'schedule workflow must belong to project');
END;

CREATE TABLE IF NOT EXISTS webhook_endpoints (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    provider TEXT NOT NULL CHECK (length(trim(provider)) > 0),
    token_hash BLOB NOT NULL CHECK (length(token_hash) > 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (project_id, name),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_webhook_endpoints_project_name
    ON webhook_endpoints (project_id, name ASC, id ASC);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id TEXT PRIMARY KEY,
    endpoint_id TEXT NOT NULL,
    provider_delivery_id TEXT NOT NULL CHECK (length(trim(provider_delivery_id)) > 0),
    event_type TEXT NOT NULL CHECK (length(trim(event_type)) > 0),
    payload_sha256 TEXT NOT NULL CHECK (length(payload_sha256) = 64),
    status TEXT NOT NULL CHECK (status IN ('received', 'accepted', 'rejected', 'failed')),
    error_message TEXT,
    received_at INTEGER NOT NULL,
    processed_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (endpoint_id, provider_delivery_id),
    FOREIGN KEY (endpoint_id) REFERENCES webhook_endpoints(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_endpoint_received
    ON webhook_deliveries (endpoint_id, received_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS deployments (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    environment TEXT NOT NULL CHECK (length(trim(environment)) > 0),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    finished_at INTEGER,
    CHECK (finished_at IS NULL OR finished_at >= created_at),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_deployments_project_created
    ON deployments (project_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_deployments_run_created
    ON deployments (run_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS deployment_events (
    id TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    reason TEXT,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (deployment_id) REFERENCES deployments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_deployment_events_deployment_created
    ON deployment_events (deployment_id, created_at ASC, id ASC);

CREATE TRIGGER IF NOT EXISTS configuration_deployments_run_project_insert
BEFORE INSERT ON deployments
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM runs WHERE id = NEW.run_id AND project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'deployment run must belong to project');
END;

CREATE TRIGGER IF NOT EXISTS configuration_deployments_run_project_update
BEFORE UPDATE OF project_id, run_id ON deployments
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM runs WHERE id = NEW.run_id AND project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'deployment run must belong to project');
END;
