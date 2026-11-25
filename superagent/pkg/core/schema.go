package core

import _ "embed"

// schema initializes a fresh Core store with the current physical format.
//
//go:embed schema/0028.sql
var schema string

const (
	issueStatus     = `json_extract(body,'$.status')`
	unfinishedIssue = issueStatus + ` NOT IN ('completed','canceled')`
)

//go:embed schema/0028-trace-associations.sql
var traceAssociationSchema string

// Core installs these indexes on startup for both new and existing stores.
//
//go:embed schema/0028-query-indexes.sql
var queryIndexSchema string

// TestStartupQueryIndexNamesMatchSchema checks this list against queryIndexSchema.
var queryIndexNames = []string{
	"session_bindings_pending",
	"session_bindings_ready_owner",
	"topics_created_at",
	"issues_topic_status",
	"issues_unfinished",
}
