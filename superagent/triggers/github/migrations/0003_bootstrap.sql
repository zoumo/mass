CREATE TABLE github_rate_limit (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	pause_until INTEGER NOT NULL
);
INSERT INTO github_rate_limit VALUES (1, 0);
CREATE TABLE github_repositories (
	name TEXT PRIMARY KEY,
	backlog INTEGER NOT NULL DEFAULT 0 CHECK (backlog >= 0),
	accepted INTEGER NOT NULL DEFAULT 0 CHECK (accepted >= 0),
	duplicates INTEGER NOT NULL DEFAULT 0 CHECK (duplicates >= 0),
	last_receipt BLOB NOT NULL DEFAULT '{}',
	last_delivery_error TEXT NOT NULL DEFAULT '',
	next_delivery_at INTEGER NOT NULL DEFAULT 0,
	delivery_failures INTEGER NOT NULL DEFAULT 0 CHECK (delivery_failures >= 0)
);
CREATE TABLE github_streams (
	repository TEXT NOT NULL REFERENCES github_repositories (name),
	scope TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('parent', 'discussion', 'ci')),
	active INTEGER NOT NULL CHECK (active IN (0, 1)),
	discussion_priority INTEGER NOT NULL DEFAULT 0,
	discussion_closed INTEGER NOT NULL DEFAULT 0,
	enabled_at INTEGER NOT NULL,
	last_success_at INTEGER NOT NULL DEFAULT 0,
	last_full_at INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	failures INTEGER NOT NULL DEFAULT 0,
	match_key TEXT NOT NULL DEFAULT '',
	silent_baseline INTEGER NOT NULL DEFAULT 0 CHECK (silent_baseline IN (0, 1)),
	item_count INTEGER NOT NULL DEFAULT 0 CHECK (item_count >= 0),
	PRIMARY KEY (repository, scope)
) WITHOUT ROWID;
CREATE INDEX github_due_streams ON github_streams
	(repository, kind, discussion_priority, last_success_at, scope, next_attempt_at) WHERE active = 1;
CREATE TABLE github_snapshots (
	repository TEXT NOT NULL,
	scope TEXT NOT NULL,
	object_key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 0),
	updated_at INTEGER NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY (repository, scope, object_key),
	FOREIGN KEY (repository, scope) REFERENCES github_streams (repository, scope)
) WITHOUT ROWID;
CREATE TRIGGER github_count_snapshot AFTER INSERT ON github_snapshots BEGIN
	UPDATE github_streams SET item_count = item_count + 1 WHERE repository = NEW.repository AND scope = NEW.scope;
END;
CREATE TRIGGER github_uncount_snapshot AFTER DELETE ON github_snapshots BEGIN
	UPDATE github_streams SET item_count = item_count - 1 WHERE repository = OLD.repository AND scope = OLD.scope;
END;
CREATE TABLE github_pending (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	repository TEXT NOT NULL REFERENCES github_repositories (name),
	message_id TEXT NOT NULL,
	payload BLOB NOT NULL,
	UNIQUE (repository, message_id)
);
CREATE INDEX github_pending_fifo ON github_pending (repository, sequence);
CREATE TRIGGER github_count_pending AFTER INSERT ON github_pending BEGIN
	UPDATE github_repositories SET backlog = backlog + 1 WHERE name = NEW.repository;
END;
CREATE TRIGGER github_uncount_pending AFTER DELETE ON github_pending BEGIN
	UPDATE github_repositories SET backlog = backlog - 1 WHERE name = OLD.repository;
END;
