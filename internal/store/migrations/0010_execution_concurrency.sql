-- Durable workflow and job concurrency groups coordinate independent gci
-- workers without requiring a broker or another service.

CREATE TABLE execution_concurrency_leases (
    scope TEXT NOT NULL CHECK (scope IN ('workflow', 'job')),
    group_key TEXT NOT NULL CHECK (length(trim(group_key)) BETWEEN 1 AND 512),
    run_id TEXT NOT NULL,
    holder_id TEXT NOT NULL CHECK (length(trim(holder_id)) > 0),
    owner_id TEXT NOT NULL CHECK (length(trim(owner_id)) > 0),
    acquired_at INTEGER NOT NULL,
    heartbeat_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (scope, group_key),
    CHECK (expires_at > heartbeat_at),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX idx_execution_concurrency_expiry
    ON execution_concurrency_leases (expires_at ASC, scope ASC, group_key ASC);

CREATE INDEX idx_execution_concurrency_run
    ON execution_concurrency_leases (run_id, scope);

CREATE TRIGGER execution_concurrency_holder_insert
BEFORE INSERT ON execution_concurrency_leases
FOR EACH ROW WHEN
    (NEW.scope = 'workflow' AND NEW.holder_id != NEW.run_id)
    OR (NEW.scope = 'job' AND NOT EXISTS (
        SELECT 1 FROM jobs WHERE id = NEW.holder_id AND run_id = NEW.run_id
    ))
BEGIN
    SELECT RAISE(ABORT, 'execution concurrency holder must belong to run');
END;

CREATE TRIGGER execution_concurrency_holder_update
BEFORE UPDATE ON execution_concurrency_leases
FOR EACH ROW WHEN
    (NEW.scope = 'workflow' AND NEW.holder_id != NEW.run_id)
    OR (NEW.scope = 'job' AND NOT EXISTS (
        SELECT 1 FROM jobs WHERE id = NEW.holder_id AND run_id = NEW.run_id
    ))
BEGIN
    SELECT RAISE(ABORT, 'execution concurrency holder must belong to run');
END;
