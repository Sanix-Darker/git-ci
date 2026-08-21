-- Workflow-command annotations are immutable diagnostics attached to the
-- existing step snapshot. Repository output remains text and is never HTML.

CREATE TABLE step_annotations (
    id TEXT PRIMARY KEY,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    level TEXT NOT NULL CHECK (level IN ('notice', 'warning', 'error')),
    message TEXT NOT NULL CHECK (length(CAST(message AS BLOB)) BETWEEN 1 AND 4096),
    title TEXT NOT NULL DEFAULT '' CHECK (length(CAST(title AS BLOB)) <= 1024),
    file TEXT NOT NULL DEFAULT '' CHECK (length(CAST(file AS BLOB)) <= 1024),
    start_line INTEGER CHECK (start_line IS NULL OR start_line > 0),
    end_line INTEGER CHECK (end_line IS NULL OR end_line > 0),
    start_column INTEGER CHECK (start_column IS NULL OR start_column > 0),
    end_column INTEGER CHECK (end_column IS NULL OR end_column > 0),
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_step_annotations_step_created
ON step_annotations(step_id, created_at, id);

CREATE TRIGGER step_annotations_limit
BEFORE INSERT ON step_annotations
WHEN (SELECT COUNT(*) FROM step_annotations WHERE step_id = NEW.step_id) >= 50
BEGIN
    SELECT RAISE(ABORT, 'step annotation limit reached');
END;
