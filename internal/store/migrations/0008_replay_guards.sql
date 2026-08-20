-- Replay runs are immutable descendants of a terminal source snapshot. These
-- guards keep provenance and duplicate suppression true for every caller.

CREATE INDEX idx_run_lineage_replay_job
    ON run_lineage (kind, source_job_id, created_at DESC)
    WHERE kind IN ('job_replay', 'step_replay');

CREATE INDEX idx_run_lineage_replay_step
    ON run_lineage (source_step_id, created_at DESC)
    WHERE kind = 'step_replay';

CREATE TRIGGER run_lineage_replay_provenance_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.kind IN ('job_replay', 'step_replay') AND NOT EXISTS (
    SELECT 1
    FROM runs AS generated
    JOIN runs AS source ON source.id = NEW.source_run_id
    WHERE generated.id = NEW.run_id
      AND generated.trigger_type = NEW.kind
      AND generated.project_id = source.project_id
      AND generated.ref IS source.ref
      AND generated.commit_sha IS source.commit_sha
      AND generated.workflow_id IS source.workflow_id
      AND generated.workflow_revision IS source.workflow_revision
      AND generated.source_path IS source.source_path
)
BEGIN
    SELECT RAISE(ABORT, 'replay run does not preserve source provenance');
END;

CREATE TRIGGER run_lineage_job_replay_shape_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.kind = 'job_replay' AND NOT EXISTS (
    SELECT 1
    FROM jobs AS source
    JOIN jobs AS generated
      ON generated.run_id = NEW.run_id
     AND generated.job_key IS source.job_key
     AND generated.name IS source.name
     AND generated.runner IS source.runner
     AND generated.environment_json IS source.environment_json
     AND generated.dependency_keys_json IS source.dependency_keys_json
     AND generated.allow_failure IS source.allow_failure
     AND generated.timeout_minutes IS source.timeout_minutes
    WHERE source.id = NEW.source_job_id
)
BEGIN
    SELECT RAISE(ABORT, 'job replay does not contain the source job snapshot');
END;

CREATE TRIGGER run_lineage_step_replay_shape_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.kind = 'step_replay' AND NOT EXISTS (
    SELECT 1
    FROM jobs AS source_job
    JOIN steps AS source_step ON source_step.id = NEW.source_step_id
    JOIN jobs AS generated_job
      ON generated_job.run_id = NEW.run_id
     AND generated_job.job_key IS source_job.job_key
     AND generated_job.name IS source_job.name
     AND generated_job.runner IS source_job.runner
     AND generated_job.environment_json IS source_job.environment_json
     AND generated_job.dependency_keys_json = '[]'
    JOIN steps AS generated_step
      ON generated_step.job_id = generated_job.id
     AND generated_step.step_key IS source_step.step_key
     AND generated_step.name IS source_step.name
     AND generated_step.command IS source_step.command
     AND generated_step.action IS source_step.action
     AND generated_step.working_directory IS source_step.working_directory
     AND generated_step.timeout_minutes IS source_step.timeout_minutes
     AND generated_step.shell IS source_step.shell
     AND generated_step.allow_failure IS source_step.allow_failure
     AND generated_step.environment_json IS source_step.environment_json
    WHERE source_job.id = NEW.source_job_id
      AND (SELECT COUNT(*) FROM jobs WHERE run_id = NEW.run_id) = 1
      AND (SELECT COUNT(*) FROM steps WHERE job_id = generated_job.id) = 1
)
BEGIN
    SELECT RAISE(ABORT, 'step replay does not isolate the source step snapshot');
END;

CREATE TRIGGER run_lineage_active_job_replay_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.kind = 'job_replay' AND EXISTS (
    SELECT 1
    FROM run_lineage AS lineage
    JOIN runs AS active ON active.id = lineage.run_id
    WHERE lineage.kind = 'job_replay'
      AND lineage.source_job_id = NEW.source_job_id
      AND active.status IN ('queued', 'waiting', 'running')
)
BEGIN
    SELECT RAISE(ABORT, 'active job replay already exists');
END;

CREATE TRIGGER run_lineage_active_step_replay_insert
BEFORE INSERT ON run_lineage
FOR EACH ROW WHEN NEW.kind = 'step_replay' AND EXISTS (
    SELECT 1
    FROM run_lineage AS lineage
    JOIN runs AS active ON active.id = lineage.run_id
    WHERE lineage.kind = 'step_replay'
      AND lineage.source_step_id = NEW.source_step_id
      AND active.status IN ('queued', 'waiting', 'running')
)
BEGIN
    SELECT RAISE(ABORT, 'active step replay already exists');
END;
