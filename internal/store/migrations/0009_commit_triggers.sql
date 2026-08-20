-- Opt-in local checkout commit observation. One policy per project keeps the
-- feature lightweight while durable checkpoints make restarts idempotent.

CREATE TABLE project_commit_triggers (
    project_id TEXT PRIMARY KEY,
    ref TEXT NOT NULL CHECK (length(trim(ref)) > 0),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    last_commit_sha TEXT,
    last_checked_at INTEGER,
    last_triggered_at INTEGER,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX idx_project_commit_triggers_enabled
    ON project_commit_triggers (enabled, updated_at ASC, project_id ASC);

CREATE INDEX idx_runs_commit_trigger_dedup
    ON runs (workflow_id, commit_sha, trigger_type)
    WHERE trigger_type = 'commit' AND workflow_id IS NOT NULL AND commit_sha IS NOT NULL;
