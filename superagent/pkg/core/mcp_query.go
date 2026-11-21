package core

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

func validIncludes(includes []string, allowed ...string) error {
	seen := map[string]bool{}
	for _, include := range includes {
		if seen[include] || !slices.Contains(allowed, include) {
			return problem("invalid_argument", "include contains an unavailable or repeated expansion")
		}
		seen[include] = true
	}
	return nil
}

func (s *Server) mcpQuery(ctx context.Context, b Binding, tool string, raw []byte) (any, error) {
	switch tool {
	case "list_messages", "list_topics", "list_issues":
		return s.mcpList(ctx, b, tool, raw)
	case "get_message":
		if b.Role == "issue" {
			return s.store.queryBoundIssue(ctx, b, "get_message", raw)
		}
		var q struct {
			MessageID string   `json:"messageId"`
			Include   []string `json:"include,omitempty"`
		}
		if err := decodeStrict(raw, &q); err != nil {
			return nil, err
		}
		allowed := []string{"thread", "replies"}
		if b.Role == "router" {
			allowed = append(allowed, "routing")
		}
		if err := validIncludes(q.Include, allowed...); err != nil {
			return nil, err
		}
		value, err := s.store.mcpRecord(ctx, b, "messages", q.MessageID)
		if err != nil {
			return nil, err
		}
		if meta := value["meta"]; len(meta) != 0 {
			value["meta"] = agentFollowupMeta(meta)
		}
		if !slices.Contains(q.Include, "routing") {
			delete(value, "routing")
			delete(value, "relations")
		}
		if slices.Contains(q.Include, "thread") {
			var thread string
			_ = json.Unmarshal(value["threadKey"], &thread)
			value["thread"], err = s.store.getScoped(ctx, "threads", thread, &b, nil)
			if err != nil {
				return nil, err
			}
		}
		if slices.Contains(q.Include, "replies") {
			value["replies"], err = s.store.mcpExpansion(ctx, b, QueryRequest{Collection: "outbox", MessageID: q.MessageID})
			if err != nil {
				return nil, err
			}
		}
		return value, nil
	case "get_topic":
		var q struct {
			Include []string `json:"include,omitempty"`
		}
		if err := decodeStrict(raw, &q); err != nil {
			return nil, err
		}
		if err := validIncludes(q.Include, "executionTemplates", "followups", "pullRequests"); err != nil {
			return nil, err
		}
		value, err := s.store.mcpRecord(ctx, b, "topics", b.OwnerID)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(q.Include, "pullRequests") {
			delete(value, "pullRequests")
		}
		if slices.Contains(q.Include, "executionTemplates") {
			value["executionTemplates"], err = json.Marshal(s.config.Execution.IssueTemplates)
			if err != nil {
				return nil, err
			}
		}
		if slices.Contains(q.Include, "followups") {
			value["followups"], err = s.store.mcpExpansion(ctx, b, QueryRequest{Collection: "timers", TopicID: b.OwnerID})
			if err != nil {
				return nil, err
			}
		}
		return value, nil
	case "get_issue":
		if b.Role == "issue" {
			value, err := s.store.queryBoundIssue(ctx, b, "get_issue", raw)
			if err != nil {
				return nil, err
			}
			receipt := value.(WorkReceipt)
			receipt.Issue.Reports, receipt.Issue.PullRequests = nil, nil
			return receipt, nil
		}
		var q struct {
			IssueID string   `json:"issueId"`
			Include []string `json:"include,omitempty"`
		}
		if err := decodeStrict(raw, &q); err != nil {
			return nil, err
		}
		if err := validIncludes(q.Include, "executions", "reports", "pullRequests"); err != nil {
			return nil, err
		}
		value, err := s.store.mcpRecord(ctx, b, "issues", q.IssueID)
		if err != nil {
			return nil, err
		}
		markMCPIssueTarget(ctx, q.IssueID)
		if !slices.Contains(q.Include, "reports") {
			delete(value, "reports")
		}
		if !slices.Contains(q.Include, "pullRequests") {
			delete(value, "pullRequests")
		}
		if slices.Contains(q.Include, "executions") {
			value["executions"], err = s.store.mcpExpansion(ctx, b, QueryRequest{Collection: "executions", IssueID: q.IssueID})
			if err != nil {
				return nil, err
			}
		}
		return value, nil
	case "get_report":
		var q struct {
			ReportID string `json:"reportId"`
		}
		if err := decodeStrict(raw, &q); err != nil {
			return nil, err
		}
		value, err := s.store.mcpRecord(ctx, b, "reports", q.ReportID)
		if err == nil {
			var issueID string
			if json.Unmarshal(value["issueId"], &issueID) == nil {
				markMCPIssueTarget(ctx, issueID)
			}
		}
		return value, err
	}
	return nil, problem("forbidden", "unknown scoped query")
}

func (s *Store) mcpRecord(ctx context.Context, b Binding, collection, id string) (map[string]json.RawMessage, error) {
	if strings.TrimSpace(id) == "" {
		return nil, problem("invalid_argument", "record ID is required")
	}
	raw, err := s.getScoped(ctx, collection, id, &b, nil)
	if err != nil {
		return nil, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, storageError()
	}
	return value, nil
}

func (s *Store) mcpExpansion(ctx context.Context, b Binding, q QueryRequest) (json.RawMessage, error) {
	var items []json.RawMessage
	q.Limit = 200
	for {
		page, err := s.listScoped(ctx, q, &b, nil)
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			break
		}
		q.Cursor = page.NextCursor
	}
	if items == nil {
		return json.RawMessage(`[]`), nil
	}
	return json.Marshal(items)
}

func (s *Server) mcpList(ctx context.Context, b Binding, tool string, raw []byte) (any, error) {
	var q QueryRequest
	switch tool {
	case "list_messages":
		var r struct {
			ThreadKey string `json:"threadKey,omitempty"`
			Cursor    string `json:"cursor,omitempty"`
			Limit     *int   `json:"limit,omitempty"`
		}
		if err := decodeStrict(raw, &r); err != nil {
			return nil, err
		}
		q = QueryRequest{Collection: "messages", ThreadKey: r.ThreadKey, Cursor: r.Cursor, Limit: 50}
		if r.Limit != nil {
			q.Limit = *r.Limit
		}
	case "list_topics":
		var r struct {
			Query  string `json:"query,omitempty"`
			Cursor string `json:"cursor,omitempty"`
			Limit  *int   `json:"limit,omitempty"`
		}
		if err := decodeStrict(raw, &r); err != nil {
			return nil, err
		}
		q = QueryRequest{Collection: "topics", search: r.Query, Cursor: r.Cursor, Limit: 50}
		if r.Limit != nil {
			q.Limit = *r.Limit
		}
	case "list_issues":
		var r struct {
			Status string `json:"status,omitempty"`
			Cursor string `json:"cursor,omitempty"`
			Limit  *int   `json:"limit,omitempty"`
		}
		if err := decodeStrict(raw, &r); err != nil {
			return nil, err
		}
		if r.Status != "" && !slices.Contains([]string{"pending", "active", "blocked", "completed", "canceled"}, r.Status) {
			return nil, problem("invalid_argument", "invalid Issue status")
		}
		q = QueryRequest{Collection: "issues", status: r.Status, Cursor: r.Cursor, Limit: 50}
		if r.Limit != nil {
			q.Limit = *r.Limit
		}
	}
	page, err := s.store.listScoped(ctx, q, &b, nil)
	if err != nil {
		return nil, err
	}
	if tool == "list_issues" {
		for i, raw := range page.Items {
			var issue map[string]json.RawMessage
			if json.Unmarshal(raw, &issue) != nil {
				return nil, storageError()
			}
			delete(issue, "reports")
			delete(issue, "pullRequests")
			page.Items[i], err = json.Marshal(issue)
			if err != nil {
				return nil, err
			}
		}
	}
	return page, nil
}
