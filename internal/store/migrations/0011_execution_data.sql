-- Execution outputs remain lightweight: SQLite owns searchable metadata while
-- immutable archive bodies live beneath the service state directory.

CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    step_id TEXT,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 255),
    format TEXT NOT NULL DEFAULT 'zip' CHECK (format = 'zip'),
    storage_key TEXT NOT NULL UNIQUE CHECK (length(trim(storage_key)) BETWEEN 1 AND 1024),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    file_count INTEGER NOT NULL CHECK (file_count >= 0),
    expires_at INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (run_id, name),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (step_id) REFERENCES steps(id) ON DELETE SET NULL
);

CREATE INDEX idx_artifacts_run_created ON artifacts (run_id, created_at DESC);
CREATE INDEX idx_artifacts_expiry ON artifacts (expires_at) WHERE expires_at IS NOT NULL;

CREATE TRIGGER artifacts_owner_insert
BEFORE INSERT ON artifacts
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1
    FROM runs r
    JOIN jobs j ON j.run_id = r.id
    WHERE r.id = NEW.run_id
      AND r.project_id = NEW.project_id
      AND j.id = NEW.job_id
      AND (
        NEW.step_id IS NULL
        OR EXISTS (SELECT 1 FROM steps s WHERE s.id = NEW.step_id AND s.job_id = j.id)
      )
)
BEGIN
    SELECT RAISE(ABORT, 'artifact owner must belong to run');
END;

CREATE TABLE cache_entries (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    ref TEXT NOT NULL CHECK (length(trim(ref)) BETWEEN 1 AND 1024),
    cache_key TEXT NOT NULL CHECK (length(trim(cache_key)) BETWEEN 1 AND 512),
    storage_key TEXT NOT NULL UNIQUE CHECK (length(trim(storage_key)) BETWEEN 1 AND 1024),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    file_count INTEGER NOT NULL CHECK (file_count >= 0),
    created_at INTEGER NOT NULL,
    accessed_at INTEGER NOT NULL,
    expires_at INTEGER,
    UNIQUE (project_id, ref, cache_key),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX idx_cache_entries_lookup
    ON cache_entries (project_id, ref, cache_key, created_at DESC);
CREATE INDEX idx_cache_entries_accessed ON cache_entries (accessed_at ASC);

CREATE TABLE test_reports (
    id TEXT PRIMARY KEY,
    artifact_id TEXT,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    step_id TEXT,
    format TEXT NOT NULL CHECK (format = 'junit'),
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 255),
    tests INTEGER NOT NULL CHECK (tests >= 0),
    failures INTEGER NOT NULL CHECK (failures >= 0),
    errors INTEGER NOT NULL CHECK (errors >= 0),
    skipped INTEGER NOT NULL CHECK (skipped >= 0),
    duration_seconds REAL NOT NULL CHECK (duration_seconds >= 0),
    created_at INTEGER NOT NULL,
    FOREIGN KEY (artifact_id) REFERENCES artifacts(id) ON DELETE SET NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (step_id) REFERENCES steps(id) ON DELETE SET NULL
);

CREATE INDEX idx_test_reports_run_created ON test_reports (run_id, created_at DESC);

CREATE TRIGGER test_reports_owner_insert
BEFORE INSERT ON test_reports
FOR EACH ROW WHEN NOT EXISTS (
    SELECT 1
    FROM runs r
    JOIN jobs j ON j.run_id = r.id
    WHERE r.id = NEW.run_id
      AND r.project_id = NEW.project_id
      AND j.id = NEW.job_id
      AND (
        NEW.step_id IS NULL
        OR EXISTS (SELECT 1 FROM steps s WHERE s.id = NEW.step_id AND s.job_id = j.id)
      )
      AND (
        NEW.artifact_id IS NULL
        OR EXISTS (SELECT 1 FROM artifacts a WHERE a.id = NEW.artifact_id AND a.run_id = r.id)
      )
)
BEGIN
    SELECT RAISE(ABORT, 'test report owner must belong to run');
END;
