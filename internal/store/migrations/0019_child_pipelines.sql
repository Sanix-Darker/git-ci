-- Repository-local GitLab child pipelines are ordinary immutable runs linked
-- to the parent bridge job. The child definition is frozen on the bridge job;
-- lineage is relational so ownership and commit/ref invariants stay in SQLite.

ALTER TABLE jobs ADD COLUMN child_pipeline_json TEXT
    CHECK (child_pipeline_json IS NULL OR json_valid(child_pipeline_json));

DROP TRIGGER IF EXISTS execution_jobs_snapshot_immutable;
CREATE TRIGGER execution_jobs_snapshot_immutable
BEFORE UPDATE OF run_id, job_key, name, runner, position, environment_json,
    dependency_keys_json, allow_failure, timeout_minutes, rollback_command,
    verification_command, child_pipeline_json ON jobs
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
    NEW.verification_command IS NOT OLD.verification_command OR
    NEW.child_pipeline_json IS NOT OLD.child_pipeline_json
BEGIN
    SELECT RAISE(ABORT, 'job snapshot is immutable');
END;

CREATE TABLE child_pipeline_links (
    parent_run_id TEXT NOT NULL,
    parent_job_id TEXT PRIMARY KEY,
    child_run_id TEXT NOT NULL UNIQUE,
    source_file TEXT NOT NULL CHECK (length(trim(source_file)) > 0),
    strategy TEXT NOT NULL CHECK (strategy IN ('async', 'mirror', 'depend')),
    depth INTEGER NOT NULL CHECK (depth BETWEEN 1 AND 2),
    created_at INTEGER NOT NULL,
    FOREIGN KEY (parent_run_id) REFERENCES runs(id) ON DELETE RESTRICT,
    FOREIGN KEY (parent_job_id) REFERENCES jobs(id) ON DELETE RESTRICT,
    FOREIGN KEY (child_run_id) REFERENCES runs(id) ON DELETE RESTRICT
);

CREATE INDEX idx_child_pipeline_parent_run
    ON child_pipeline_links (parent_run_id, created_at ASC, child_run_id ASC);
CREATE INDEX idx_child_pipeline_child_run
    ON child_pipeline_links (child_run_id, parent_run_id);

CREATE TRIGGER child_pipeline_link_insert
BEFORE INSERT ON child_pipeline_links
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1
    FROM runs AS parent
    JOIN jobs AS bridge ON bridge.id = NEW.parent_job_id AND bridge.run_id = parent.id
    JOIN runs AS child ON child.id = NEW.child_run_id
    WHERE parent.id = NEW.parent_run_id
      AND parent.id <> child.id
      AND parent.project_id = child.project_id
      AND parent.workflow_id IS child.workflow_id
      AND parent.workflow_key IS child.workflow_key
      AND parent.workflow_revision IS child.workflow_revision
      AND parent.ref IS child.ref
      AND parent.commit_sha IS child.commit_sha
      AND parent.status = 'running'
      AND bridge.status = 'queued'
      AND bridge.child_pipeline_json IS NOT NULL
      AND json_extract(bridge.child_pipeline_json, '$.sourceFile') = NEW.source_file
      AND json_extract(bridge.child_pipeline_json, '$.strategy') = NEW.strategy
      AND child.status = 'queued'
      AND child.trigger_type = 'parent_pipeline'
      AND NEW.depth = COALESCE((
          SELECT ancestor.depth + 1
          FROM child_pipeline_links AS ancestor
          WHERE ancestor.child_run_id = parent.id
      ), 1)
)
BEGIN
    SELECT RAISE(ABORT, 'child pipeline lineage is not compatible');
END;

CREATE TRIGGER child_pipeline_links_immutable
BEFORE UPDATE ON child_pipeline_links
FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'child pipeline lineage is immutable');
END;
