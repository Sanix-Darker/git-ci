-- Provenance for rollback and future replay runs. Generated graphs remain
-- ordinary runs; lineage records why they were cloned and makes retries safe.

ALTER TABLE jobs ADD COLUMN rollback_command TEXT;
ALTER TABLE jobs ADD COLUMN verification_command TEXT;

DROP TRIGGER IF EXISTS execution_jobs_snapshot_immutable;
CREATE TRIGGER execution_jobs_snapshot_immutable
BEFORE UPDATE OF run_id, job_key, name, runner, position, environment_json,
    dependency_keys_json, allow_failure, timeout_minutes, rollback_command,
    verification_command ON jobs
FOR EACH ROW WHEN
    NEW.run_id IS NOT OLD.run_id OR
    NEW.job_key IS NOT OLD.job_key OR
    NEW.name IS NOT OLD.name OR
    NEW.runner IS NOT OLD.runner OR
    NEW.position IS NOT OLD.position OR
    NEW.environment_json IS NOT OLD.environment_json OR
    NEW.dependency_keys_json IS NOT OLD.dependency_keys_json OR
    NEW.allow_failure IS NOT OLD.allow_failure OR
    NEW.timeout_minutes IS NOT OLD.timeout_minutes OR
    NEW.rollback_command IS NOT OLD.rollback_command OR
    NEW.verification_command IS NOT OLD.verification_command
BEGIN
    SELECT RAISE(ABORT, 'job snapshot is immutable');
END;

ALTER TABLE deployments ADD COLUMN source_deployment_id TEXT
    REFERENCES deployments(id) ON DELETE SET NULL;
ALTER TABLE deployments ADD COLUMN target_deployment_id TEXT
    REFERENCES deployments(id) ON DELETE SET NULL;

CREATE TRIGGER deployments_lineage_immutable
BEFORE UPDATE OF source_deployment_id, target_deployment_id ON deployments
FOR EACH ROW WHEN
    NEW.source_deployment_id IS NOT OLD.source_deployment_id OR
    NEW.target_deployment_id IS NOT OLD.target_deployment_id
BEGIN
    SELECT RAISE(ABORT, 'deployment lineage is immutable');
END;

CREATE TABLE run_lineage (
    run_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('rollback', 'job_replay', 'step_replay')),
    source_run_id TEXT NOT NULL,
    source_job_id TEXT,
    source_step_id TEXT,
    source_deployment_id TEXT,
    target_deployment_id TEXT,
    actor TEXT NOT NULL CHECK (length(trim(actor)) > 0),
    idempotency_key TEXT NOT NULL CHECK (length(trim(idempotency_key)) > 0),
    created_at INTEGER NOT NULL,
    UNIQUE (actor, idempotency_key),
    CHECK (
        (kind = 'rollback' AND source_job_id IS NULL AND source_step_id IS NULL
            AND source_deployment_id IS NOT NULL AND target_deployment_id IS NOT NULL) OR
        (kind = 'job_replay' AND source_job_id IS NOT NULL AND source_step_id IS NULL
            AND source_deployment_id IS NULL AND target_deployment_id IS NULL) OR
        (kind = 'step_replay' AND source_job_id IS NOT NULL AND source_step_id IS NOT NULL
            AND source_deployment_id IS NULL AND target_deployment_id IS NULL)
    ),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (source_run_id) REFERENCES runs(id) ON DELETE RESTRICT,
    FOREIGN KEY (source_job_id) REFERENCES jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (source_step_id) REFERENCES steps(id) ON DELETE RESTRICT,
    FOREIGN KEY (source_deployment_id) REFERENCES deployments(id) ON DELETE RESTRICT,
    FOREIGN KEY (target_deployment_id) REFERENCES deployments(id) ON DELETE RESTRICT
);

CREATE INDEX idx_run_lineage_source ON run_lineage (source_run_id, kind, created_at DESC);
CREATE INDEX idx_run_lineage_rollback ON run_lineage (source_deployment_id, target_deployment_id, created_at DESC)
    WHERE kind = 'rollback';

CREATE TRIGGER run_lineage_project_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1 FROM runs AS generated
    JOIN runs AS source ON source.id = NEW.source_run_id
    WHERE generated.id = NEW.run_id AND generated.project_id = source.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'lineage runs must belong to one project');
END;

CREATE TRIGGER run_lineage_job_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.source_job_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM jobs WHERE id = NEW.source_job_id AND run_id = NEW.source_run_id
)
BEGIN
    SELECT RAISE(ABORT, 'lineage job must belong to source run');
END;

CREATE TRIGGER run_lineage_step_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.source_step_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM steps WHERE id = NEW.source_step_id AND job_id = NEW.source_job_id
)
BEGIN
    SELECT RAISE(ABORT, 'lineage step must belong to source job');
END;

CREATE TRIGGER run_lineage_rollback_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.kind = 'rollback' AND NOT EXISTS (
    SELECT 1
    FROM runs AS generated
    JOIN deployments AS source ON source.id = NEW.source_deployment_id
    JOIN deployments AS target ON target.id = NEW.target_deployment_id
    JOIN runs AS target_run ON target_run.id = target.run_id
    WHERE generated.id = NEW.run_id
      AND generated.project_id = source.project_id
      AND source.run_id = NEW.source_run_id
      AND source.project_id = target.project_id
      AND source.environment = target.environment
      AND source.id <> target.id
      AND target.status = 'succeeded'
      AND generated.commit_sha IS target_run.commit_sha
      AND generated.workflow_id IS target_run.workflow_id
      AND generated.workflow_revision IS target_run.workflow_revision
)
BEGIN
    SELECT RAISE(ABORT, 'rollback deployments are not compatible');
END;

CREATE TRIGGER run_lineage_active_rollback_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.kind = 'rollback' AND EXISTS (
    SELECT 1 FROM run_lineage AS lineage
    JOIN runs AS active ON active.id = lineage.run_id
    WHERE lineage.kind = 'rollback'
      AND lineage.source_deployment_id = NEW.source_deployment_id
      AND lineage.target_deployment_id = NEW.target_deployment_id
      AND active.status IN ('queued', 'waiting', 'running')
)
BEGIN
    SELECT RAISE(ABORT, 'active rollback already exists');
END;

CREATE TRIGGER run_lineage_immutable
BEFORE UPDATE ON run_lineage
FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'run lineage is immutable');
END;
