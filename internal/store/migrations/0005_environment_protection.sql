-- Durable environment protection. Execution consumes these records in the
-- resumable worker migration; this schema keeps policy and decisions auditable.

CREATE TABLE IF NOT EXISTS environments (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    deployment_tier TEXT NOT NULL DEFAULT 'other'
        CHECK (deployment_tier IN ('production', 'staging', 'testing', 'development', 'other')),
    protected INTEGER NOT NULL DEFAULT 0 CHECK (protected IN (0, 1)),
    required_approvals INTEGER NOT NULL DEFAULT 0 CHECK (required_approvals IN (0, 1)),
    wait_timer_seconds INTEGER NOT NULL DEFAULT 0 CHECK (wait_timer_seconds BETWEEN 0 AND 86400),
    allowed_refs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(allowed_refs_json)),
    concurrency_mode TEXT NOT NULL DEFAULT 'queue'
        CHECK (concurrency_mode IN ('queue', 'cancel_in_progress')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (project_id, name),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_environments_project_name
    ON environments (project_id, name ASC, id ASC);

CREATE TABLE IF NOT EXISTS environment_approval_requests (
    id TEXT PRIMARY KEY,
    environment_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    job_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    required_approvals INTEGER NOT NULL CHECK (required_approvals IN (1)),
    requested_by TEXT NOT NULL CHECK (length(trim(requested_by)) > 0),
    requested_at INTEGER NOT NULL,
    decided_at INTEGER,
    CHECK (decided_at IS NULL OR decided_at >= requested_at),
    FOREIGN KEY (environment_id) REFERENCES environments(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_environment_approvals_pending
    ON environment_approval_requests (status, requested_at ASC, id ASC);

CREATE TRIGGER IF NOT EXISTS environment_approvals_target_insert
BEFORE INSERT ON environment_approval_requests
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1
    FROM deployment_targets AS target
    JOIN runs AS run ON run.id = target.run_id
    JOIN environments AS environment
      ON environment.id = NEW.environment_id
     AND environment.project_id = run.project_id
     AND environment.name = target.environment
    WHERE target.job_id = NEW.job_id AND target.run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'approval request must match deployment target');
END;

CREATE TABLE IF NOT EXISTS environment_approval_decisions (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    decision TEXT NOT NULL CHECK (decision IN ('approved', 'rejected')),
    actor TEXT NOT NULL CHECK (length(trim(actor)) > 0),
    reason TEXT,
    created_at INTEGER NOT NULL,
    UNIQUE (request_id),
    FOREIGN KEY (request_id) REFERENCES environment_approval_requests(id) ON DELETE CASCADE
);

CREATE TRIGGER IF NOT EXISTS environment_approval_decisions_immutable
BEFORE UPDATE ON environment_approval_decisions
FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'approval decision is immutable');
END;

CREATE TABLE IF NOT EXISTS environment_secret_envelopes (
    id TEXT PRIMARY KEY,
    environment_id TEXT NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    provider TEXT,
    version TEXT,
    encryption_algorithm TEXT NOT NULL CHECK (length(trim(encryption_algorithm)) > 0),
    nonce BLOB NOT NULL CHECK (length(nonce) > 0),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (environment_id, name),
    FOREIGN KEY (environment_id) REFERENCES environments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_environment_secrets_name
    ON environment_secret_envelopes (environment_id, name ASC, id ASC);

CREATE TABLE IF NOT EXISTS environment_leases (
    environment_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    job_id TEXT NOT NULL UNIQUE,
    owner_id TEXT NOT NULL CHECK (length(trim(owner_id)) > 0),
    acquired_at INTEGER NOT NULL,
    heartbeat_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    CHECK (heartbeat_at >= acquired_at),
    CHECK (expires_at > heartbeat_at),
    FOREIGN KEY (environment_id) REFERENCES environments(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_environment_leases_expiry
    ON environment_leases (expires_at ASC, environment_id ASC);

CREATE TRIGGER IF NOT EXISTS environment_leases_target_insert
BEFORE INSERT ON environment_leases
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1
    FROM deployment_targets AS target
    JOIN runs AS run ON run.id = target.run_id
    JOIN environments AS environment
      ON environment.id = NEW.environment_id
     AND environment.project_id = run.project_id
     AND environment.name = target.environment
    WHERE target.job_id = NEW.job_id AND target.run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'environment lease must match deployment target');
END;

CREATE TRIGGER IF NOT EXISTS environment_leases_target_update
BEFORE UPDATE OF environment_id, run_id, job_id ON environment_leases
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1
    FROM deployment_targets AS target
    JOIN runs AS run ON run.id = target.run_id
    JOIN environments AS environment
      ON environment.id = NEW.environment_id
     AND environment.project_id = run.project_id
     AND environment.name = target.environment
    WHERE target.job_id = NEW.job_id AND target.run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'environment lease must match deployment target');
END;
