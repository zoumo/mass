CREATE TABLE threads (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 source TEXT NOT NULL, native_id TEXT NOT NULL, created_at TEXT NOT NULL,
 last_message_at TEXT NOT NULL, subscriptions TEXT NOT NULL DEFAULT '[]',
 UNIQUE(source,native_id)
);
CREATE TABLE trigger_messages (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT GENERATED ALWAYS AS ('mass-m' || seq) STORED NOT NULL UNIQUE,
 source TEXT NOT NULL, account_id TEXT NOT NULL, native_id TEXT NOT NULL,
 thread_id TEXT NOT NULL REFERENCES threads(id), accepted_at TEXT NOT NULL,
 canonical BLOB NOT NULL, body BLOB NOT NULL, routing_state TEXT,
 scheduled_task_id TEXT REFERENCES scheduled_tasks(id),
 scheduled_task_revision INTEGER,
 scheduled_trigger TEXT CHECK(scheduled_trigger IN ('scheduled','manual')),
 scheduled_for TEXT,
 UNIQUE(source,account_id,native_id)
);
CREATE INDEX trigger_messages_thread ON trigger_messages(thread_id,accepted_at,id);
CREATE INDEX trigger_messages_scheduled_task ON trigger_messages(scheduled_task_id,accepted_at,id) WHERE scheduled_task_id IS NOT NULL;
CREATE INDEX trigger_messages_source ON trigger_messages(source,accepted_at,id);
CREATE TABLE topics (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT GENERATED ALWAYS AS ('mass-t' || seq) STORED NOT NULL UNIQUE,
 created_at TEXT NOT NULL, body BLOB NOT NULL,
 creation_key TEXT NOT NULL UNIQUE, creation_digest TEXT NOT NULL,
 issue_number INTEGER NOT NULL DEFAULT 0
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
CREATE UNIQUE INDEX current_issue_session_binding ON session_bindings(owner_id) WHERE role='issue' AND state<>'revoked';
CREATE INDEX session_bindings_stop ON session_bindings(id) WHERE json_extract(body,'$.stop.status')='requested';
CREATE TABLE scheduled_tasks (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT GENERATED ALWAYS AS ('mass-st' || seq) STORED NOT NULL UNIQUE,
 owner_type TEXT CHECK(owner_type='topic'), owner_id TEXT REFERENCES topics(id),
 topic_generation INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, schedule_anchor TEXT NOT NULL, deleted_at TEXT, body BLOB NOT NULL,
 status TEXT GENERATED ALWAYS AS (body->>'$.status') STORED NOT NULL CHECK(status IN ('active','paused','completed')),
 next_run_at TEXT GENERATED ALWAYS AS (body->>'$.nextRunAt') STORED,
 creation_key TEXT NOT NULL UNIQUE, creation_digest TEXT NOT NULL,
 CHECK((owner_type IS NULL) = (owner_id IS NULL)),
 CHECK((status='active') = (next_run_at IS NOT NULL))
);
CREATE INDEX scheduled_tasks_owner ON scheduled_tasks(owner_type,owner_id,created_at,id) WHERE deleted_at IS NULL;
CREATE INDEX scheduled_tasks_due ON scheduled_tasks(next_run_at,seq) WHERE status='active' AND deleted_at IS NULL;
CREATE TABLE outbox (
 id TEXT PRIMARY KEY NOT NULL,
 message_id TEXT NOT NULL REFERENCES trigger_messages(id),
 content TEXT NOT NULL CHECK (CASE WHEN typeof(content)='text' AND json_valid(content)
 THEN json_type(content)='object' ELSE 0 END),
 status TEXT NOT NULL CHECK(status IN ('pending','sent','failed')),
 receipt TEXT,
 blocker_ref TEXT
);
CREATE INDEX outbox_message ON outbox(message_id);
CREATE VIEW reports AS
 SELECT CAST(json_extract(r.value,'$.sequence') AS INTEGER) AS seq, json_extract(r.value,'$.id') AS id,
 json_extract(r.value,'$.sessionBindingId') AS session_binding_id, issues.id AS issue_id,
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

CREATE TABLE internal_events (
 event_id TEXT PRIMARY KEY NOT NULL, kind TEXT NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 content TEXT CHECK (content IS NULL OR (CASE WHEN typeof(content)='text' AND json_valid(content)
 THEN json_type(content)='object' ELSE 0 END)),
 body TEXT, refs TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','dispatching','sent','superseded')), sent_at TEXT
);
CREATE INDEX internal_events_pending ON internal_events(json_extract(recipient,'$.kind'), COALESCE(json_extract(recipient,'$.topic_id'),json_extract(recipient,'$.session_binding_id'),''))
 WHERE status IN ('pending','dispatching');

CREATE INDEX internal_events_route_message ON internal_events(json_extract(refs,'$[0].id'))
 WHERE kind IN ('routed_message','route_request');
CREATE UNIQUE INDEX internal_events_return ON internal_events(json_extract(refs,'$[1].id'))
 WHERE kind='route_request' AND json_extract(refs,'$[1].id') IS NOT NULL;

CREATE VIEW thread_subscriptions AS
 SELECT t.seq, t.id AS thread_id, json_extract(sub.value,'$.id') AS id,
 json_extract(sub.value,'$.topicId') AS topic_id,
 json_extract(sub.value,'$.createdAt') AS created_at, sub.value AS body
 FROM threads t,json_each(t.subscriptions) sub;

CREATE VIEW routing_events AS
 SELECT rowid AS seq, event_id, kind, created_at, status, sent_at, sender, recipient, content, body, refs,
 json_extract(refs,'$[0].id') AS message_id, json_extract(refs,'$[1].id') AS returned_event_id,
 coalesce(json_extract(recipient,'$.topic_id'),json_extract(sender,'$.topic_id')) AS topic_id
 FROM internal_events WHERE kind IN ('routed_message','route_request');

CREATE VIEW routing_relations AS
 SELECT d.seq,d.event_id AS id,d.message_id,d.topic_id,d.created_at,
 json_object('id',d.event_id,'messageId',d.message_id,'topicId',d.topic_id,'createdAt',d.created_at,
 'action',CASE d.kind WHEN 'routed_message' THEN 'received' ELSE 'returned' END,
 'status',d.status,'eventId',coalesce(d.returned_event_id,d.event_id),'reason',json_extract(d.body,'$'),
 'returned',json(CASE WHEN EXISTS(SELECT 1 FROM internal_events r WHERE r.kind='route_request'
 AND json_extract(refs,'$[1].id')=d.event_id) THEN 'true' ELSE 'false' END)) AS body
 FROM routing_events d WHERE d.topic_id IS NOT NULL;

CREATE VIEW topic_message_history AS
 SELECT message_id,topic_id FROM routing_events WHERE kind='routed_message';
