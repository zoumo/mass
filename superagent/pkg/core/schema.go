package core

// Schema 21 is initialized in a new directory. Earlier databases remain in
// their original format and are never opened for writing by this version.
const schema = `
CREATE TABLE threads (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE,
 source TEXT NOT NULL, native_id TEXT NOT NULL, created_at TEXT NOT NULL,
 last_message_at TEXT NOT NULL, subscriptions TEXT NOT NULL DEFAULT '[]',
 UNIQUE(source,native_id)
);
CREATE TABLE trigger_messages (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT GENERATED ALWAYS AS ('mass-m' || seq) STORED NOT NULL UNIQUE,
 source TEXT NOT NULL, account_id TEXT NOT NULL, native_id TEXT NOT NULL,
 thread_key TEXT NOT NULL REFERENCES threads(key), accepted_at TEXT NOT NULL,
 canonical BLOB NOT NULL, body BLOB NOT NULL, routing_state TEXT,
 UNIQUE(source,account_id,native_id)
);
CREATE INDEX trigger_messages_thread ON trigger_messages(thread_key,accepted_at,id);
CREATE INDEX trigger_messages_echo_hold ON trigger_messages(json_extract(body,'$.echo.deadline'))
 WHERE json_extract(body,'$.echo.status')='held';
CREATE TABLE topics (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT GENERATED ALWAYS AS ('mass-t' || seq) STORED NOT NULL UNIQUE,
 created_at TEXT NOT NULL, body BLOB NOT NULL,
 creation_key TEXT NOT NULL UNIQUE, creation_digest TEXT NOT NULL,
 issue_number INTEGER NOT NULL DEFAULT 0, timer_number INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE session_bindings (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, role TEXT NOT NULL, owner_id TEXT NOT NULL, generation INTEGER NOT NULL, created_at TEXT NOT NULL, token TEXT NOT NULL UNIQUE, spec BLOB NOT NULL, state TEXT NOT NULL, next_attempt_at TEXT NOT NULL DEFAULT '', body BLOB NOT NULL, UNIQUE(role,owner_id,generation));
CREATE UNIQUE INDEX sole_router ON session_bindings(role) WHERE role='router' AND state<>'revoked';
CREATE TABLE issues (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, topic_id TEXT NOT NULL REFERENCES topics(id),
 number INTEGER NOT NULL CHECK(number>0),
 id TEXT GENERATED ALWAYS AS (topic_id || '-i' || number) STORED NOT NULL UNIQUE,
 created_at TEXT NOT NULL, body BLOB NOT NULL,
 creation_key TEXT NOT NULL UNIQUE, creation_digest TEXT NOT NULL,
 report_creations TEXT NOT NULL DEFAULT '{}', UNIQUE(topic_id,number)
);
CREATE INDEX issues_topic ON issues(topic_id,created_at,id);
CREATE TABLE executions (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, issue_id TEXT NOT NULL REFERENCES issues(id),
 binding_id TEXT NOT NULL UNIQUE REFERENCES session_bindings(id), generation INTEGER NOT NULL,
 created_at TEXT NOT NULL, body BLOB NOT NULL, UNIQUE(issue_id,generation)
);

CREATE INDEX session_bindings_stop ON session_bindings(id) WHERE json_extract(body,'$.stop.status')='requested';
CREATE TABLE scheduled_tasks (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, topic_id TEXT NOT NULL REFERENCES topics(id),
 number INTEGER NOT NULL CHECK(number>0),
 id TEXT GENERATED ALWAYS AS (topic_id || '-tm' || number) STORED NOT NULL UNIQUE,
 created_at TEXT NOT NULL, body BLOB NOT NULL, trigger_message_id TEXT REFERENCES trigger_messages(id),
 creation_key TEXT NOT NULL UNIQUE, creation_digest TEXT NOT NULL,
 UNIQUE(topic_id,number)
);
CREATE INDEX scheduled_tasks_topic ON scheduled_tasks(topic_id,created_at,id);
CREATE INDEX scheduled_tasks_due ON scheduled_tasks(` + timerDueAt + `,seq) WHERE ` + timerStatus + `='scheduled';
CREATE TABLE outbox (
 id TEXT PRIMARY KEY NOT NULL,
 message_id TEXT NOT NULL REFERENCES trigger_messages(id),
 text TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','sent','failed')),
 receipt TEXT,
 blocker_ref TEXT
);
CREATE INDEX outbox_message ON outbox(message_id);
CREATE INDEX outbox_native_receipt ON outbox(json_extract(receipt,'$.conversationId'),json_extract(receipt,'$.messageId'))
 WHERE status='sent';
CREATE VIEW reports AS
 SELECT CAST(json_extract(r.value,'$.sequence') AS INTEGER) AS seq, json_extract(r.value,'$.id') AS id,
 json_extract(r.value,'$.executionId') AS execution_id, issues.id AS issue_id,
 json_extract(r.value,'$.createdAt') AS created_at, r.value AS body
 FROM issues, json_each(issues.body,'$.reports') r;
CREATE VIEW work_pull_requests AS
 SELECT CAST(json_extract(p.value,'$.sequence') AS INTEGER) AS seq, json_extract(p.value,'$.id') AS id,
 t.id AS topic_id, NULL AS issue_id, json_extract(p.value,'$.createdAt') AS created_at,
 json_remove(p.value,'$.sequence') AS body
 FROM topics t,json_each(t.body,'$.pullRequests') p
 UNION ALL
 SELECT CAST(json_extract(p.value,'$.sequence') AS INTEGER), json_extract(p.value,'$.id'),
 i.topic_id, i.id, json_extract(p.value,'$.createdAt'), json_remove(p.value,'$.sequence')
 FROM issues i,json_each(i.body,'$.pullRequests') p;
` + internalDeliverySchema + routingViews
