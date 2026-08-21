-- GitHub-compatible step summaries are small Markdown documents kept beside
-- the immutable step snapshot. API and UI consumers receive text, never HTML.

ALTER TABLE steps
ADD COLUMN summary TEXT NOT NULL DEFAULT ''
CHECK (length(CAST(summary AS BLOB)) <= 1048576);
