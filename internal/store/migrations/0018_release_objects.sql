-- Releases are durable CD records derived from a successful run and an
-- operator-managed Git tag. Artifacts and deployments remain normalized and
-- are linked through run_id rather than copied into this table.

CREATE TABLE releases (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    tag_name TEXT NOT NULL CHECK (length(tag_name) BETWEEN 1 AND 255),
    target_commit_sha TEXT NOT NULL CHECK (length(target_commit_sha) IN (40, 64)),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 256),
    notes TEXT NOT NULL DEFAULT '' CHECK (length(notes) <= 65536),
    state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'published')),
    prerelease INTEGER NOT NULL DEFAULT 0 CHECK (prerelease IN (0, 1)),
    created_by TEXT NOT NULL CHECK (length(created_by) BETWEEN 1 AND 256),
    published_by TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    published_at INTEGER,
    UNIQUE (project_id, tag_name),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT,
    CHECK (
        (state = 'draft' AND published_by IS NULL AND published_at IS NULL)
        OR
        (state = 'published' AND published_by IS NOT NULL AND published_at IS NOT NULL)
    )
);

CREATE INDEX idx_releases_project_state_time
    ON releases (project_id, state, prerelease, published_at DESC, created_at DESC);

CREATE INDEX idx_releases_state_time
    ON releases (state, published_at DESC, created_at DESC);

CREATE TRIGGER releases_source_run_insert
BEFORE INSERT ON releases
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1
    FROM runs
    WHERE id = NEW.run_id
      AND project_id = NEW.project_id
      AND status = 'succeeded'
      AND lower(commit_sha) = lower(NEW.target_commit_sha)
)
BEGIN
    SELECT RAISE(ABORT, 'release source run invariant failed');
END;

CREATE TRIGGER releases_immutable_provenance_update
BEFORE UPDATE ON releases
FOR EACH ROW WHEN
    NEW.project_id != OLD.project_id
    OR NEW.run_id != OLD.run_id
    OR NEW.tag_name != OLD.tag_name
    OR NEW.target_commit_sha != OLD.target_commit_sha
    OR NEW.created_by != OLD.created_by
    OR NEW.created_at != OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'release provenance is immutable');
END;

CREATE TRIGGER releases_transition_update
BEFORE UPDATE ON releases
FOR EACH ROW WHEN
    NEW.state != OLD.state
    AND NOT (OLD.state = 'draft' AND NEW.state = 'published')
BEGIN
    SELECT RAISE(ABORT, 'release state transition is invalid');
END;

CREATE TRIGGER releases_published_update
BEFORE UPDATE ON releases
FOR EACH ROW WHEN
    OLD.state = 'published'
    AND (
        NEW.name != OLD.name
        OR NEW.notes != OLD.notes
        OR NEW.prerelease != OLD.prerelease
        OR NEW.state != OLD.state
        OR NEW.published_by != OLD.published_by
        OR NEW.published_at != OLD.published_at
    )
BEGIN
    SELECT RAISE(ABORT, 'published release is immutable');
END;
