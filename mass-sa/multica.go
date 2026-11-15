package sa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	multicaCallTimeout = 60 * time.Second
	multicaOutputLimit = 1 << 20
	multicaStderrLimit = 2 << 10

	// MetadataProject names the mass-sa project on every bridged Issue.
	MetadataProject = "mass_sa_project"
	// MetadataTicketID names the Ticket represented by an Issue.
	MetadataTicketID = "mass_sa_ticket_id"
	// MetadataAssignmentID names the worker Assignment that currently owns a ticket.
	MetadataAssignmentID = "mass_sa_assignment_id"
	// MetadataResolution records the explicit ticket resolution.
	MetadataResolution = "mass_sa_resolution"
)

// MulticaTarget is the explicit workspace and project a bridge writes to.
type MulticaTarget struct {
	WorkspaceID string
	ProjectID   string
}

// CreateIssueInput is one issue create request, always without an assignee.
type CreateIssueInput struct {
	Title       string
	Description string
	ParentID    string
}

// IssueSnapshot is the authoritative Issue state read back from the CLI.
type IssueSnapshot struct {
	ID         string
	Identifier string
	Title      string
	Status     string
	ProjectID  string
	ParentID   string
}

// CommentSnapshot identifies one comment confirmed by readback.
type CommentSnapshot struct {
	ID string
}

// IssueTracker is the narrow external bridge used by the ticket business layer.
// Implementations must confirm every write through an authoritative CLI readback
// and must never report an unconfirmed write as complete.
type IssueTracker interface {
	CreateIssue(context.Context, CreateIssueInput) (IssueSnapshot, error)
	GetIssue(context.Context, string) (IssueSnapshot, error)
	SetMetadata(context.Context, string, map[string]string) error
	AddComment(context.Context, string, string) (CommentSnapshot, error)
	ChangeStatus(context.Context, string, string) (IssueSnapshot, error)
}

// MulticaCLI drives the multica executable with exec args only. It never uses a
// shell, never passes file content as an argument, and never relies on the CLI
// default workspace.
type MulticaCLI struct {
	executable string
	target     MulticaTarget
}

// NewMulticaCLI validates and binds one executable to one explicit target.
func NewMulticaCLI(executable string, target MulticaTarget) (*MulticaCLI, error) {
	if err := validateSingleLine("multica.executable", executable, true); err != nil {
		return nil, err
	}
	if err := validateSingleLine("multica.workspaceId", target.WorkspaceID, true); err != nil {
		return nil, err
	}
	if err := validateSingleLine("multica.projectId", target.ProjectID, true); err != nil {
		return nil, err
	}
	return &MulticaCLI{executable: executable, target: target}, nil
}

// CreateIssue creates a parentless or child Issue and confirms it by readback.
// No assignee is ever passed, so creation does not enqueue a Multica agent.
func (c *MulticaCLI) CreateIssue(ctx context.Context, input CreateIssueInput) (IssueSnapshot, error) {
	if err := validateSingleLine("issue title", input.Title, true); err != nil {
		return IssueSnapshot{}, err
	}
	if err := validateBlockText("issue description", input.Description, true); err != nil {
		return IssueSnapshot{}, err
	}
	if input.ParentID != "" {
		if err := validateIssueID(input.ParentID); err != nil {
			return IssueSnapshot{}, err
		}
	}
	dir, err := prepareMulticaFile("description.md", input.Description)
	if err != nil {
		return IssueSnapshot{}, err
	}
	defer os.RemoveAll(dir)

	args := []string{
		"issue", "create",
		"--title=" + input.Title,
		"--description-file=description.md",
		"--project=" + c.target.ProjectID,
	}
	if input.ParentID != "" {
		args = append(args, "--parent="+input.ParentID)
	}
	args = append(args, "--output=json")

	raw, err := c.execute(ctx, dir, true, args)
	if err != nil {
		return IssueSnapshot{}, err
	}
	created, err := decodeIssue(raw)
	if err != nil || created.ID == "" {
		return IssueSnapshot{}, operationError("outcome_unknown", "multica_outcome_unknown", "multica issue create returned no usable issue identity; confirm the issue before retrying")
	}

	snapshot, err := c.GetIssue(ctx, created.ID)
	if err != nil {
		return IssueSnapshot{}, unconfirmedWrite(err)
	}
	if snapshot.ID != created.ID ||
		snapshot.ProjectID != c.target.ProjectID ||
		snapshot.ParentID != input.ParentID ||
		snapshot.Title != input.Title {
		return IssueSnapshot{}, operationError("failed_precondition", "multica_readback_mismatch", "multica issue readback does not match the requested issue")
	}
	if snapshot.Status == "done" || snapshot.Status == "cancelled" {
		return IssueSnapshot{}, operationError("failed_precondition", "multica_readback_mismatch", "new multica issue is already closed")
	}
	return snapshot, nil
}

// GetIssue reads one Issue by ID.
func (c *MulticaCLI) GetIssue(ctx context.Context, issueID string) (IssueSnapshot, error) {
	if err := validateIssueID(issueID); err != nil {
		return IssueSnapshot{}, err
	}
	raw, err := c.execute(ctx, "", false, []string{"issue", "get", issueID, "--output=json"})
	if err != nil {
		return IssueSnapshot{}, err
	}
	issue, err := decodeIssue(raw)
	if err != nil || issue.ID == "" {
		return IssueSnapshot{}, operationError("unavailable", "multica_invalid_output", "multica issue read returned no usable issue identity")
	}
	return issue, nil
}

// SetMetadata writes each key as a string and confirms all of them by readback.
func (c *MulticaCLI) SetMetadata(ctx context.Context, issueID string, values map[string]string) error {
	if err := validateIssueID(issueID); err != nil {
		return err
	}
	if len(values) == 0 {
		return operationError("invalid_argument", "invalid_metadata", "at least one metadata key is required")
	}
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if err := validateMetadataKey(key); err != nil {
			return err
		}
		if err := validateSingleLine("metadata value", value, true); err != nil {
			return err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		_, err := c.execute(ctx, "", true, []string{
			"issue", "metadata", "set", issueID,
			"--key=" + key,
			"--value=" + values[key],
			"--type=string",
			"--output=json",
		})
		if err != nil {
			return err
		}
	}

	raw, err := c.execute(ctx, "", false, []string{"issue", "metadata", "list", issueID, "--output=json"})
	if err != nil {
		return unconfirmedWrite(err)
	}
	actual, err := decodeMetadata(raw)
	if err != nil {
		return operationError("outcome_unknown", "multica_outcome_unknown", "multica issue metadata readback is not usable; confirm the metadata before retrying")
	}
	for _, key := range keys {
		if actual[key] != values[key] {
			return operationError("failed_precondition", "multica_readback_mismatch", "multica issue metadata readback does not match the requested value for "+key)
		}
	}
	return nil
}

// AddComment writes a top-level comment and confirms its identity by readback.
func (c *MulticaCLI) AddComment(ctx context.Context, issueID, body string) (CommentSnapshot, error) {
	if err := validateIssueID(issueID); err != nil {
		return CommentSnapshot{}, err
	}
	if err := validateBlockText("comment body", body, true); err != nil {
		return CommentSnapshot{}, err
	}
	dir, err := prepareMulticaFile("comment.md", body)
	if err != nil {
		return CommentSnapshot{}, err
	}
	defer os.RemoveAll(dir)

	raw, err := c.execute(ctx, dir, true, []string{
		"issue", "comment", "add", issueID,
		"--content-file=comment.md",
		"--output=json",
	})
	if err != nil {
		return CommentSnapshot{}, err
	}
	created, err := decodeComment(raw)
	if err != nil || created.ID == "" {
		return CommentSnapshot{}, operationError("outcome_unknown", "multica_outcome_unknown", "multica comment add returned no usable comment identity; confirm the comment before retrying")
	}

	listRaw, err := c.execute(ctx, "", false, []string{
		"issue", "comment", "list", issueID,
		"--roots-only",
		"--summary",
		"--compact",
		"--output=json",
	})
	if err != nil {
		return CommentSnapshot{}, unconfirmedWrite(err)
	}
	comments, err := decodeComments(listRaw)
	if err != nil {
		return CommentSnapshot{}, operationError("outcome_unknown", "multica_outcome_unknown", "multica comment readback is not usable; confirm the comment before retrying")
	}
	for _, comment := range comments {
		if comment.ID == created.ID {
			return CommentSnapshot{ID: created.ID}, nil
		}
	}
	return CommentSnapshot{}, operationError("failed_precondition", "multica_readback_mismatch", "multica comment readback does not contain the created comment")
}

// ChangeStatus applies an explicit status with --no-start and confirms the
// resulting status by readback.
func (c *MulticaCLI) ChangeStatus(ctx context.Context, issueID, status string) (IssueSnapshot, error) {
	if err := validateIssueID(issueID); err != nil {
		return IssueSnapshot{}, err
	}
	if !validIssueStatus(status) {
		return IssueSnapshot{}, operationError("invalid_argument", "invalid_issue_status", "unsupported multica issue status")
	}
	_, err := c.execute(ctx, "", true, []string{
		"issue", "status", issueID, status,
		"--no-start",
		"--output=json",
	})
	if err != nil {
		return IssueSnapshot{}, err
	}
	snapshot, err := c.GetIssue(ctx, issueID)
	if err != nil {
		return IssueSnapshot{}, unconfirmedWrite(err)
	}
	if snapshot.ID != issueID || snapshot.Status != status {
		return IssueSnapshot{}, operationError("failed_precondition", "multica_readback_mismatch", "multica issue status readback does not match the requested status")
	}
	return snapshot, nil
}

func (c *MulticaCLI) execute(ctx context.Context, dir string, write bool, args []string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, operationError("unavailable", "multica_canceled", "multica CLI call was canceled before it started")
	}
	callCtx, cancel := context.WithTimeout(ctx, multicaCallTimeout)
	defer cancel()

	fullArgs := append([]string{"--workspace-id=" + c.target.WorkspaceID}, args...)
	cmd := exec.CommandContext(callCtx, c.executable, fullArgs...)
	cmd.Dir = dir
	var stdout, stderr cappedBuffer
	stdout.limit = multicaOutputLimit
	stderr.limit = multicaStderrLimit
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	runErr := cmd.Run()
	if runErr == nil {
		return stdout.Bytes(), nil
	}
	if callCtx.Err() != nil {
		return nil, operationError("outcome_unknown", "multica_outcome_unknown", "multica CLI call did not complete; its outcome is unknown")
	}

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return nil, operationError("unavailable", "multica_unavailable", "multica CLI executable could not be started")
	}
	message := "multica CLI reported a failure"
	if detail := summarizeStderr(stderr.Bytes()); detail != "" {
		message += ": " + detail
	}
	if write {
		message += " (the write is not confirmed; reconcile the issue before retrying)"
	}
	return nil, operationError("unavailable", "multica_command_failed", message)
}

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(content []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		return len(content), nil
	}
	if len(content) > remaining {
		_, _ = b.buf.Write(content[:remaining])
		return len(content), nil
	}
	return b.buf.Write(content)
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func prepareMulticaFile(name, content string) (string, error) {
	if err := validateTextContent("multica file content", content); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "mass-sa-multica-*")
	if err != nil {
		return "", operationError("unavailable", "multica_workdir_unavailable", "multica CLI working directory cannot be created")
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", operationError("unavailable", "multica_workdir_unavailable", "multica CLI input file cannot be written")
	}
	return dir, nil
}

func summarizeStderr(raw []byte) string {
	text := strings.TrimSpace(strings.ToValidUTF8(string(raw), ""))
	if text == "" {
		return ""
	}
	fields := strings.Fields(text)
	joined := strings.Join(fields, " ")
	runes := []rune(joined)
	if len(runes) > 300 {
		joined = string(runes[:300]) + "..."
	}
	return joined
}

func unconfirmedWrite(err error) error {
	return operationError("outcome_unknown", "multica_outcome_unknown", "multica issue write was not confirmed by readback; inspect the issue before retrying ("+err.Error()+")")
}

type issuePayload struct {
	ID            string `json:"id"`
	Identifier    string `json:"identifier"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	ProjectID     string `json:"project_id"`
	ParentIssueID string `json:"parent_issue_id"`
}

func decodeIssue(raw []byte) (IssueSnapshot, error) {
	var payload issuePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return IssueSnapshot{}, err
	}
	return IssueSnapshot{
		ID:         payload.ID,
		Identifier: payload.Identifier,
		Title:      payload.Title,
		Status:     payload.Status,
		ProjectID:  payload.ProjectID,
		ParentID:   payload.ParentIssueID,
	}, nil
}

type commentPayload struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	ParentID string `json:"parent_id"`
}

func decodeComment(raw []byte) (commentPayload, error) {
	var payload commentPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return commentPayload{}, err
	}
	return payload, nil
}

func decodeComments(raw []byte) ([]commentPayload, error) {
	var payloads []commentPayload
	if err := json.Unmarshal(raw, &payloads); err != nil {
		return nil, err
	}
	return payloads, nil
}

func decodeMetadata(raw []byte) (map[string]string, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	decoded := make(map[string]string, len(values))
	for key, value := range values {
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			continue
		}
		decoded[key] = text
	}
	return decoded, nil
}

func validateIssueID(value string) error {
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, " \t\r\n\x00") {
		return operationError("invalid_argument", "invalid_issue_id", "multica issue ID is invalid")
	}
	return nil
}

func validateMetadataKey(key string) error {
	if key == "" || strings.HasPrefix(key, "-") {
		return operationError("invalid_argument", "invalid_metadata_key", "metadata key is invalid")
	}
	for _, r := range key {
		if r != '_' && !unicode.IsLower(r) && !unicode.IsDigit(r) {
			return operationError("invalid_argument", "invalid_metadata_key", "metadata key must be lowercase ASCII with underscores")
		}
	}
	return nil
}

// validateSingleLine accepts non-blank UTF-8 text without NUL or line breaks.
// Multica flags that carry titles, identities, and metadata values stay on one
// argument each.
func validateSingleLine(name, value string, required bool) error {
	if err := validateTextContent(name, value); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" && required {
		return operationError("invalid_argument", "invalid_argument", name+" must not be blank")
	}
	if strings.ContainsAny(value, "\r\n") {
		return operationError("invalid_argument", "invalid_argument", name+" must be a single line")
	}
	return nil
}

func validateBlockText(name, value string, required bool) error {
	if err := validateTextContent(name, value); err != nil {
		return err
	}
	if required && strings.TrimSpace(value) == "" {
		return operationError("invalid_argument", "invalid_argument", name+" must not be blank")
	}
	return nil
}

func validateTextContent(name, value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return operationError("invalid_argument", "invalid_text", name+" must be valid UTF-8 without NUL")
	}
	return nil
}

func validIssueStatus(status string) bool {
	switch status {
	case "backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled":
		return true
	default:
		return false
	}
}
