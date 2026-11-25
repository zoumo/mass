CREATE TABLE dws_submissions (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id TEXT NOT NULL,
	native_message_id TEXT NOT NULL,
	observed_at TEXT NOT NULL,
	payload BLOB NOT NULL,
	failure TEXT,
	UNIQUE (account_id, native_message_id)
);
CREATE INDEX dws_pending_sequence ON dws_submissions(sequence) WHERE failure IS NULL;
