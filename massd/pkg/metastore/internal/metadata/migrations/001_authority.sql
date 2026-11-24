CREATE TABLE authority (format INTEGER NOT NULL, cursor_key TEXT NOT NULL);
CREATE TABLE runtime (name TEXT PRIMARY KEY, type TEXT NOT NULL, state TEXT NOT NULL, payload BLOB NOT NULL);
CREATE TABLE session (key TEXT PRIMARY KEY, session_id TEXT UNIQUE NOT NULL, namespace TEXT NOT NULL, name TEXT NOT NULL, runtime_ref TEXT NOT NULL, state TEXT NOT NULL, sort_key TEXT NOT NULL, payload BLOB NOT NULL);
CREATE UNIQUE INDEX session_name ON session(namespace,name);
