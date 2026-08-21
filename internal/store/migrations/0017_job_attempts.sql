-- Automatic retries retain one logical job while recording every execution
-- attempt. Attempt rows are append-only evidence; mutable job and step status
-- continues to describe the current/final logical job state.

CREATE TABLE job_attempts (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    failure_kind TEXT,
    exit_code INTEGER CHECK (exit_code IS NULL OR exit_code >= 0),
    message TEXT,
    will_retry INTEGER NOT NULL DEFAULT 0 CHECK (will_retry IN (0, 1)),
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    UNIQUE (job_id, attempt_number),
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX idx_job_attempts_run
    ON job_attempts (run_id, job_id, attempt_number ASC);

CREATE TRIGGER job_attempts_job_run_insert
BEFORE INSERT ON job_attempts
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM jobs WHERE id = NEW.job_id AND run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'job attempt must belong to run');
END;

