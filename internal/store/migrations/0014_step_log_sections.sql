-- Provider log grouping is stored separately from immutable log lines so API
-- consumers retain the original ordered stream and can opt into structure.

CREATE TABLE step_log_sections (
    id TEXT PRIMARY KEY,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider IN ('github', 'gitlab')),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 1024),
    depth INTEGER NOT NULL CHECK (depth BETWEEN 0 AND 31),
    collapsed INTEGER NOT NULL DEFAULT 0 CHECK (collapsed IN (0, 1)),
    start_sequence INTEGER NOT NULL CHECK (start_sequence > 0),
    end_sequence INTEGER CHECK (end_sequence IS NULL OR end_sequence >= start_sequence),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_step_log_sections_step_sequence
ON step_log_sections(step_id, start_sequence, id);

CREATE TRIGGER step_log_sections_limit
BEFORE INSERT ON step_log_sections
WHEN (SELECT COUNT(*) FROM step_log_sections WHERE step_id = NEW.step_id) >= 100
BEGIN
    SELECT RAISE(ABORT, 'step log section limit reached');
END;
