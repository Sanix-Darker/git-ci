-- Durable execution state. This migration extends the initial placeholder
-- run/job/step tables in place so databases created by 0001 retain all data.

CREATE TABLE IF NOT EXISTS workflows (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    workflow_key TEXT NOT NULL CHECK (length(trim(workflow_key)) > 0),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    definition_json TEXT NOT NULL CHECK (json_valid(definition_json)),
    environment_json TEXT NOT NULL CHECK (json_valid(environment_json)),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (project_id, workflow_key),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_workflows_project_key
    ON workflows (project_id, active, workflow_key ASC, id ASC);

ALTER TABLE runs ADD COLUMN workflow_id TEXT
    REFERENCES workflows(id) ON DELETE RESTRICT;
ALTER TABLE runs ADD COLUMN workflow_key TEXT;
ALTER TABLE runs ADD COLUMN workflow_revision INTEGER;
ALTER TABLE runs ADD COLUMN environment_json TEXT NOT NULL DEFAULT '{}'
    CHECK (json_valid(environment_json));
ALTER TABLE runs ADD COLUMN cancellation_requested INTEGER NOT NULL DEFAULT 0
    CHECK (cancellation_requested IN (0, 1));
ALTER TABLE runs ADD COLUMN cancellation_requested_at INTEGER;
ALTER TABLE runs ADD COLUMN worker_id TEXT;
ALTER TABLE runs ADD COLUMN claimed_at INTEGER;
ALTER TABLE runs ADD COLUMN failure_reason TEXT;
ALTER TABLE runs ADD COLUMN source_path TEXT NOT NULL DEFAULT '';

ALTER TABLE jobs ADD COLUMN job_key TEXT;
ALTER TABLE jobs ADD COLUMN environment_json TEXT NOT NULL DEFAULT '{}'
    CHECK (json_valid(environment_json));
ALTER TABLE jobs ADD COLUMN dependency_keys_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(dependency_keys_json));
ALTER TABLE jobs ADD COLUMN allow_failure INTEGER NOT NULL DEFAULT 0
    CHECK (allow_failure IN (0, 1));
ALTER TABLE jobs ADD COLUMN timeout_minutes INTEGER NOT NULL DEFAULT 0
    CHECK (timeout_minutes >= 0);

ALTER TABLE steps ADD COLUMN step_key TEXT;
ALTER TABLE steps ADD COLUMN environment_json TEXT NOT NULL DEFAULT '{}'
    CHECK (json_valid(environment_json));
ALTER TABLE steps ADD COLUMN action TEXT;
ALTER TABLE steps ADD COLUMN working_directory TEXT;
ALTER TABLE steps ADD COLUMN timeout_minutes INTEGER NOT NULL DEFAULT 0
    CHECK (timeout_minutes >= 0);
ALTER TABLE steps ADD COLUMN shell TEXT;
ALTER TABLE steps ADD COLUMN allow_failure INTEGER NOT NULL DEFAULT 0
    CHECK (allow_failure IN (0, 1));

CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_run_key
    ON jobs (run_id, job_key)
    WHERE job_key IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_steps_job_key
    ON steps (job_id, step_key)
    WHERE step_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_runs_queue
    ON runs (status, cancellation_requested, created_at ASC, id ASC);

CREATE INDEX IF NOT EXISTS idx_runs_workflow_created_at
    ON runs (workflow_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS run_log_counters (
    run_id TEXT PRIMARY KEY,
    next_sequence INTEGER NOT NULL CHECK (next_sequence > 0),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS run_log_lines (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    stream TEXT NOT NULL CHECK (stream IN ('stdout', 'stderr', 'system')),
    message TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (run_id, sequence),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (step_id) REFERENCES steps(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_log_lines_step_sequence
    ON run_log_lines (step_id, sequence ASC, id ASC);

CREATE TRIGGER IF NOT EXISTS execution_runs_status_insert
BEFORE INSERT ON runs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution run status must be valid');
END;

CREATE TRIGGER IF NOT EXISTS execution_runs_status_update
BEFORE UPDATE OF status ON runs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution run status must be valid');
END;

CREATE TRIGGER IF NOT EXISTS execution_jobs_status_insert
BEFORE INSERT ON jobs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution job status must be valid');
END;

CREATE TRIGGER IF NOT EXISTS execution_jobs_status_update
BEFORE UPDATE OF status ON jobs
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution job status must be valid');
END;

CREATE TRIGGER IF NOT EXISTS execution_steps_status_insert
BEFORE INSERT ON steps
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution step status must be valid');
END;

CREATE TRIGGER IF NOT EXISTS execution_steps_status_update
BEFORE UPDATE OF status ON steps
FOR EACH ROW WHEN NEW.status NOT IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')
BEGIN
    SELECT RAISE(ABORT, 'execution step status must be valid');
END;

CREATE TRIGGER IF NOT EXISTS execution_runs_snapshot_immutable
BEFORE UPDATE OF project_id, workflow_id, workflow_key, workflow_revision, trigger_type, ref, commit_sha, environment_json, source_path ON runs
FOR EACH ROW WHEN
    NEW.project_id IS NOT OLD.project_id OR
    NEW.workflow_id IS NOT OLD.workflow_id OR
    NEW.workflow_key IS NOT OLD.workflow_key OR
    NEW.workflow_revision IS NOT OLD.workflow_revision OR
    NEW.trigger_type IS NOT OLD.trigger_type OR
    NEW.ref IS NOT OLD.ref OR
    NEW.commit_sha IS NOT OLD.commit_sha OR
    NEW.environment_json IS NOT OLD.environment_json OR
    NEW.source_path IS NOT OLD.source_path
BEGIN
    SELECT RAISE(ABORT, 'run snapshot is immutable');
END;

CREATE TRIGGER IF NOT EXISTS execution_jobs_snapshot_immutable
BEFORE UPDATE OF run_id, job_key, name, runner, position, environment_json, dependency_keys_json, allow_failure, timeout_minutes ON jobs
FOR EACH ROW WHEN
    NEW.run_id IS NOT OLD.run_id OR
    NEW.job_key IS NOT OLD.job_key OR
    NEW.name IS NOT OLD.name OR
    NEW.runner IS NOT OLD.runner OR
    NEW.position IS NOT OLD.position OR
    NEW.environment_json IS NOT OLD.environment_json OR
    NEW.dependency_keys_json IS NOT OLD.dependency_keys_json OR
    NEW.allow_failure IS NOT OLD.allow_failure OR
    NEW.timeout_minutes IS NOT OLD.timeout_minutes
BEGIN
    SELECT RAISE(ABORT, 'job snapshot is immutable');
END;

CREATE TRIGGER IF NOT EXISTS execution_steps_snapshot_immutable
BEFORE UPDATE OF job_id, step_index, step_key, name, command, environment_json, action, working_directory, timeout_minutes, shell, allow_failure ON steps
FOR EACH ROW WHEN
    NEW.job_id IS NOT OLD.job_id OR
    NEW.step_index IS NOT OLD.step_index OR
    NEW.step_key IS NOT OLD.step_key OR
    NEW.name IS NOT OLD.name OR
    NEW.command IS NOT OLD.command OR
    NEW.environment_json IS NOT OLD.environment_json OR
    NEW.action IS NOT OLD.action OR
    NEW.working_directory IS NOT OLD.working_directory OR
    NEW.timeout_minutes IS NOT OLD.timeout_minutes OR
    NEW.shell IS NOT OLD.shell OR
    NEW.allow_failure IS NOT OLD.allow_failure
BEGIN
    SELECT RAISE(ABORT, 'step snapshot is immutable');
END;
