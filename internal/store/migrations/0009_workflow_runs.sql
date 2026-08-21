-- Durable GitHub workflow_run completion dispatch and immutable CI-to-CD
-- provenance. The update trigger intentionally does not backfill terminal
-- runs that existed before this migration.

CREATE TABLE workflow_run_dispatches (
    source_run_id TEXT PRIMARY KEY,
    source_workflow_name TEXT NOT NULL CHECK (length(trim(source_workflow_name)) > 0),
    source_workflow_revision INTEGER NOT NULL CHECK (source_workflow_revision >= 0),
    conclusion TEXT NOT NULL CHECK (conclusion IN ('success', 'failure', 'cancelled', 'skipped')),
    created_at INTEGER NOT NULL,
    dispatched_at INTEGER,
    FOREIGN KEY (source_run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX idx_workflow_run_dispatch_pending
    ON workflow_run_dispatches (created_at, source_run_id)
    WHERE dispatched_at IS NULL;

CREATE TABLE workflow_run_links (
    run_id TEXT PRIMARY KEY,
    source_run_id TEXT NOT NULL,
    source_workflow_name TEXT NOT NULL CHECK (length(trim(source_workflow_name)) > 0),
    source_workflow_revision INTEGER NOT NULL CHECK (source_workflow_revision >= 0),
    source_conclusion TEXT NOT NULL CHECK (source_conclusion IN ('success', 'failure', 'cancelled', 'skipped')),
    target_workflow_id TEXT NOT NULL,
    target_workflow_revision INTEGER NOT NULL CHECK (target_workflow_revision > 0),
    depth INTEGER NOT NULL CHECK (depth BETWEEN 1 AND 3),
    idempotency_key TEXT NOT NULL UNIQUE CHECK (length(trim(idempotency_key)) > 0),
    created_at INTEGER NOT NULL,
    UNIQUE (source_run_id, target_workflow_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (source_run_id) REFERENCES runs(id) ON DELETE RESTRICT,
    FOREIGN KEY (target_workflow_id) REFERENCES workflows(id) ON DELETE RESTRICT
);

CREATE INDEX idx_workflow_run_links_source
    ON workflow_run_links (source_run_id, depth, created_at DESC);

CREATE TRIGGER workflow_run_dispatch_terminal
AFTER UPDATE OF status ON runs
FOR EACH ROW WHEN
    OLD.status NOT IN ('succeeded', 'failed', 'cancelled', 'skipped') AND
    NEW.status IN ('succeeded', 'failed', 'cancelled', 'skipped') AND
    NEW.workflow_id IS NOT NULL
BEGIN
    INSERT OR IGNORE INTO workflow_run_dispatches (
        source_run_id, source_workflow_name, source_workflow_revision,
        conclusion, created_at
    )
    SELECT
        NEW.id,
        workflows.name,
        COALESCE(NEW.workflow_revision, 0),
        CASE NEW.status
            WHEN 'succeeded' THEN 'success'
            WHEN 'failed' THEN 'failure'
            WHEN 'cancelled' THEN 'cancelled'
            ELSE 'skipped'
        END,
        COALESCE(NEW.finished_at, NEW.updated_at)
    FROM workflows
    WHERE workflows.id = NEW.workflow_id;
END;

CREATE TRIGGER workflow_run_link_provenance_insert
BEFORE INSERT ON workflow_run_links
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1
    FROM runs AS generated
    JOIN runs AS source ON source.id = NEW.source_run_id
    JOIN workflow_run_dispatches AS dispatch ON dispatch.source_run_id = source.id
    WHERE generated.id = NEW.run_id
      AND generated.trigger_type = 'workflow_run'
      AND generated.project_id = source.project_id
      AND generated.ref IS source.ref
      AND generated.commit_sha IS source.commit_sha
      AND generated.source_path IS source.source_path
      AND generated.workflow_id = NEW.target_workflow_id
      AND generated.workflow_revision = NEW.target_workflow_revision
      AND source.status IN ('succeeded', 'failed', 'cancelled', 'skipped')
      AND dispatch.dispatched_at IS NULL
      AND dispatch.source_workflow_name = NEW.source_workflow_name
      AND dispatch.source_workflow_revision = NEW.source_workflow_revision
      AND dispatch.conclusion = NEW.source_conclusion
      AND NEW.depth = COALESCE((
          SELECT depth FROM workflow_run_links WHERE run_id = source.id
      ), 0) + 1
)
BEGIN
    SELECT RAISE(ABORT, 'workflow_run link does not preserve completion provenance');
END;

CREATE TRIGGER workflow_run_links_immutable
BEFORE UPDATE ON workflow_run_links
FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, 'workflow_run link is immutable');
END;
