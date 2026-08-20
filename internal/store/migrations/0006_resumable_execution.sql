-- Durable waits and worker leases let protected jobs resume without replaying
-- an active command after a crash.

DROP TRIGGER IF EXISTS execution_runs_status_insert;
DROP TRIGGER IF EXISTS execution_runs_status_update;
DROP TRIGGER IF EXISTS execution_jobs_status_insert;
DROP TRIGGER IF EXISTS execution_jobs_status_update;

CREATE TRIGGER execution_runs_status_insert
BEFORE INSERT ON runs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'waiting', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution run status must be valid');
END;

CREATE TRIGGER execution_runs_status_update
BEFORE UPDATE OF status ON runs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'waiting', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution run status must be valid');
END;

CREATE TRIGGER execution_jobs_status_insert
BEFORE INSERT ON jobs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'waiting', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution job status must be valid');
END;

CREATE TRIGGER execution_jobs_status_update
BEFORE UPDATE OF status ON jobs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'waiting', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution job status must be valid');
END;

CREATE TABLE IF NOT EXISTS job_waits (
    job_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN ('approval', 'timer', 'concurrency')),
    detail TEXT,
    available_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_job_waits_reason_available
    ON job_waits (reason, available_at ASC, updated_at ASC, job_id ASC);

CREATE TRIGGER IF NOT EXISTS job_waits_job_run_insert
BEFORE INSERT ON job_waits
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM jobs WHERE id = NEW.job_id AND run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'job wait must belong to run');
END;

CREATE TABLE IF NOT EXISTS run_worker_leases (
    run_id TEXT PRIMARY KEY,
    worker_id TEXT NOT NULL CHECK (length(trim(worker_id)) > 0),
    heartbeat_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    CHECK (expires_at > heartbeat_at),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_worker_leases_expiry
    ON run_worker_leases (expires_at ASC, run_id ASC);

CREATE TRIGGER IF NOT EXISTS run_worker_leases_claim_insert
BEFORE INSERT ON run_worker_leases
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id = NEW.run_id AND status = 'running' AND worker_id = NEW.worker_id
)
BEGIN
    SELECT RAISE(ABORT, 'run worker lease must match active claim');
END;

CREATE TRIGGER IF NOT EXISTS run_worker_leases_claim_update
BEFORE UPDATE ON run_worker_leases
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id = NEW.run_id AND status = 'running' AND worker_id = NEW.worker_id
)
BEGIN
    SELECT RAISE(ABORT, 'run worker lease must match active claim');
END;
