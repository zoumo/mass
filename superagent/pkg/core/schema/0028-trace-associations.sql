CREATE TABLE IF NOT EXISTS trace_associations (
 collection TEXT NOT NULL,
 object_id TEXT NOT NULL,
 phase TEXT NOT NULL,
 revision INTEGER NOT NULL,
 traceparent TEXT NOT NULL CHECK (length(traceparent) = 55),
 tracestate TEXT NOT NULL CHECK (length(tracestate) <= 512),
 created_at TEXT NOT NULL,
 PRIMARY KEY (collection, object_id, phase, revision)
) WITHOUT ROWID;
