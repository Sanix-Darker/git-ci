-- Manual jobs pause and resume the same immutable run. Effective rule state
-- and operator play provenance live beside snapshots rather than mutating them.

DROP TRIGGER IF EXISTS execution_jobs_status_insert;
DROP TRIGGER IF EXISTS execution_jobs_status_update;

CREATE TRIGGER execution_jobs_status_insert
BEFORE INSERT ON jobs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'waiting', 'manual', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution job status must be valid');
END;

CREATE TRIGGER execution_jobs_status_update
BEFORE UPDATE OF status ON jobs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'waiting', 'manual', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution job status must be valid');
END;

CREATE TABLE manual_job_states (
    job_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    blocking INTEGER NOT NULL CHECK (blocking IN (0, 1)),
    confirmation TEXT,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX idx_manual_job_states_run
    ON manual_job_states (run_id, created_at ASC, job_id ASC);

CREATE TABLE manual_job_plays (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    actor TEXT NOT NULL CHECK (length(trim(actor)) > 0),
    idempotency_key TEXT NOT NULL CHECK (length(trim(idempotency_key)) > 0),
    variables_json TEXT NOT NULL CHECK (json_valid(variables_json) AND json_type(variables_json) = 'object'),
    confirmed INTEGER NOT NULL CHECK (confirmed IN (0, 1)),
    created_at INTEGER NOT NULL,
    UNIQUE (job_id, idempotency_key),
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX idx_manual_job_plays_run
    ON manual_job_plays (run_id, created_at DESC, id DESC);

CREATE TRIGGER manual_job_states_job_run_insert
BEFORE INSERT ON manual_job_states
FOR EACH ROW WHEN NOT EXISTS (SELECT 1 FROM jobs WHERE id = NEW.job_id AND run_id = NEW.run_id)
BEGIN
    SELECT RAISE(ABORT, 'manual job state must belong to run');
END;

CREATE TRIGGER manual_job_plays_job_run_insert
BEFORE INSERT ON manual_job_plays
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM manual_job_states WHERE job_id = NEW.job_id AND run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'manual job play must belong to paused job');
END;
