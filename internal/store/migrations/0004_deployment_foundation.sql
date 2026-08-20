-- Immutable deployment targets connect workflow jobs to environment history.
-- Policy, approvals, leases, and resumable execution build on this snapshot.

CREATE TABLE IF NOT EXISTS deployment_targets (
    job_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    job_key TEXT NOT NULL CHECK (length(trim(job_key)) > 0),
    environment TEXT NOT NULL CHECK (length(trim(environment)) > 0),
    deployment_tier TEXT NOT NULL DEFAULT 'other'
        CHECK (deployment_tier IN ('production', 'staging', 'testing', 'development', 'other')),
    created_at INTEGER NOT NULL,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_deployment_targets_run_job
    ON deployment_targets (run_id, job_key ASC, job_id ASC);

CREATE TRIGGER IF NOT EXISTS deployment_targets_job_run_insert
BEFORE INSERT ON deployment_targets
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM jobs WHERE id = NEW.job_id AND run_id = NEW.run_id AND job_key = NEW.job_key
)
BEGIN
    SELECT RAISE(ABORT, 'deployment target job must belong to run');
END;

CREATE TRIGGER IF NOT EXISTS deployment_targets_snapshot_immutable
BEFORE UPDATE ON deployment_targets
FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'deployment target snapshot is immutable');
END;

ALTER TABLE deployments ADD COLUMN job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL;
ALTER TABLE deployments ADD COLUMN deployment_tier TEXT NOT NULL DEFAULT 'other'
    CHECK (deployment_tier IN ('production', 'staging', 'testing', 'development', 'other'));

CREATE UNIQUE INDEX IF NOT EXISTS idx_deployments_job
    ON deployments (job_id)
    WHERE job_id IS NOT NULL;

CREATE TRIGGER IF NOT EXISTS deployments_job_run_insert
BEFORE INSERT ON deployments
FOR EACH ROW WHEN NEW.job_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM jobs WHERE id = NEW.job_id AND run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'deployment job must belong to run');
END;

CREATE TRIGGER IF NOT EXISTS deployments_job_run_update
BEFORE UPDATE OF job_id, run_id ON deployments
FOR EACH ROW WHEN NEW.job_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM jobs WHERE id = NEW.job_id AND run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'deployment job must belong to run');
END;
