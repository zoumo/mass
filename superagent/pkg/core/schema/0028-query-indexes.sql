CREATE INDEX IF NOT EXISTS session_bindings_pending ON session_bindings(next_attempt_at,id) WHERE state='pending';
CREATE INDEX IF NOT EXISTS session_bindings_ready_owner ON session_bindings(role,owner_id,id) WHERE state='ready';
CREATE INDEX IF NOT EXISTS topics_created_at ON topics(created_at,id);
CREATE INDEX IF NOT EXISTS issues_topic_status ON issues(topic_id,json_extract(body,'$.status'),created_at,id);
CREATE INDEX IF NOT EXISTS issues_unfinished ON issues(topic_id) WHERE json_extract(body,'$.status') NOT IN ('completed','canceled');
