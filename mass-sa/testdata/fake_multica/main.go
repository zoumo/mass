// Command fake_multica is a test-only Multica CLI replacement. It records every
// invocation, keeps a small issue store, and supports deterministic failure,
// hang, and readback anomalies. It is never built into mass-sa.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type issue struct {
	ID             string            `json:"id"`
	Identifier     string            `json:"identifier"`
	Number         int               `json:"number"`
	Title          string            `json:"title"`
	Description    string            `json:"description"`
	Status         string            `json:"status"`
	StatusCategory string            `json:"status_category"`
	ProjectID      string            `json:"project_id"`
	ParentIssueID  string            `json:"parent_issue_id,omitempty"`
	Metadata       map[string]string `json:"metadata"`
	WorkspaceID    string            `json:"workspace_id"`
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
	AssigneeID     string            `json:"assignee_id,omitempty"`
	AssigneeType   string            `json:"assignee_type,omitempty"`
}

type comment struct {
	ID               string  `json:"id"`
	IssueID          string  `json:"issue_id"`
	Content          string  `json:"content"`
	ParentID         *string `json:"parent_id"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	ReplyCount       int     `json:"reply_count,omitempty"`
	ContentTruncated bool    `json:"content_truncated,omitempty"`
}

type stateFile struct {
	NextIssue   int                   `json:"nextIssue"`
	NextComment int                   `json:"nextComment"`
	Issues      map[string]*issue     `json:"issues"`
	Comments    map[string][]*comment `json:"comments"`
}

type invocation struct {
	Args []string `json:"args"`
	CWD  string   `json:"cwd"`
}

func main() {
	args := os.Args[1:]
	cwd, _ := os.Getwd()
	logPath := strings.TrimSpace(os.Getenv("FAKE_MULTICA_LOG"))
	statePath := strings.TrimSpace(os.Getenv("FAKE_MULTICA_STATE"))
	workspace := strings.TrimSpace(os.Getenv("FAKE_MULTICA_WORKSPACE"))
	project := strings.TrimSpace(os.Getenv("FAKE_MULTICA_PROJECT"))
	if logPath == "" || statePath == "" || workspace == "" {
		fatalf("fake multica requires FAKE_MULTICA_LOG, FAKE_MULTICA_STATE, and FAKE_MULTICA_WORKSPACE")
	}
	recordInvocation(logPath, invocation{Args: args, CWD: cwd})

	actualWorkspace, ok := flagValue(args, "workspace-id")
	if !ok || actualWorkspace != workspace {
		fatalf("workspace-id must be %s", workspace)
	}
	commandIndex := -1
	for i, arg := range args {
		if arg == "issue" {
			commandIndex = i
			break
		}
	}
	if commandIndex < 0 || len(args) < commandIndex+2 {
		fatalf("unsupported command")
	}
	commandArgs := args[commandIndex:]

	// Independent CLI processes share this fixture. Serialize the complete
	// read-modify-write transaction, including read commands, so a late reader
	// cannot overwrite a concurrent create with an older snapshot.
	lock, err := os.OpenFile(statePath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fatalf("fake state lock cannot be opened")
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		fatalf("fake state cannot be locked")
	}
	state := loadState(statePath)
	command := commandArgs[1]
	if command == "metadata" || command == "comment" {
		if len(commandArgs) < 3 {
			fatalf("unsupported command")
		}
		command = command + " " + commandArgs[2]
	}
	fail := failCommand(command)
	failAfterWrite := fail && os.Getenv("FAKE_MULTICA_FAIL_AFTER_WRITE") == "1"
	if fail && !failAfterWrite {
		fatalf("injected failure for %s", command)
	}

	switch command {
	case "create":
		handleCreate(state, workspace, project, commandArgs)
	case "get":
		handleGet(state, commandArgs)
	case "metadata set":
		handleMetadataSet(state, commandArgs)
	case "metadata list":
		handleMetadataList(state, commandArgs)
	case "comment add":
		handleCommentAdd(state, commandArgs)
	case "comment list":
		handleCommentList(state, commandArgs)
	case "status":
		handleStatus(state, commandArgs)
	default:
		fatalf("unsupported command %q", command)
	}
	saveState(statePath, state)
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		fatalf("fake state cannot be unlocked")
	}
	if failAfterWrite {
		fatalf("injected failure after write for %s", command)
	}
	maybeHang(command)
}

func handleCreate(state *stateFile, workspace, project string, args []string) {
	title, ok := flagValue(args, "title")
	if !ok || strings.TrimSpace(title) == "" {
		fatalf("--title is required")
	}
	projectID, ok := flagValue(args, "project")
	if !ok || (project != "" && projectID != project) {
		fatalf("--project must be %s", project)
	}
	if _, assigned := flagValue(args, "assignee"); assigned {
		fatalf("issue create must not assign an agent")
	}
	if _, assigned := flagValue(args, "assignee-id"); assigned {
		fatalf("issue create must not assign an agent")
	}
	cwd, _ := os.Getwd()
	description := ""
	if rel, ok := flagValue(args, "description-file"); ok {
		description = readWorkdirFile(cwd, rel)
	} else if inline, ok := flagValue(args, "description"); ok {
		description = inline
	}
	parent, _ := flagValue(args, "parent")
	if parent != "" {
		if _, ok := state.Issues[parent]; !ok {
			fatalf("parent issue %s not found", parent)
		}
	}

	state.NextIssue++
	number := state.NextIssue
	now := time.Now().UTC().Format(time.RFC3339)
	created := &issue{
		ID:             fmt.Sprintf("00000000-0000-4000-8000-%012d", number),
		Identifier:     "MASS-" + strconv.Itoa(number),
		Number:         number,
		Title:          title,
		Description:    description,
		Status:         "todo",
		StatusCategory: "todo",
		ProjectID:      projectID,
		ParentIssueID:  parent,
		Metadata:       map[string]string{},
		WorkspaceID:    workspace,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	state.Issues[created.ID] = created
	printJSON(created)
}

func handleGet(state *stateFile, args []string) {
	if len(args) < 3 {
		fatalf("issue get requires an issue ID")
	}
	printJSON(requireIssue(state, args[2]))
}

func handleMetadataSet(state *stateFile, args []string) {
	if len(args) < 4 {
		fatalf("issue metadata set requires an issue ID")
	}
	target := requireIssue(state, args[3])
	key, ok := flagValue(args, "key")
	if !ok || key == "" {
		fatalf("--key is required")
	}
	value, ok := flagValue(args, "value")
	if !ok {
		fatalf("--value is required")
	}
	valueType, _ := flagValue(args, "type")
	if valueType != "string" {
		fatalf("--type must be string")
	}
	if os.Getenv("FAKE_MULTICA_DROP_METADATA") != key {
		target.Metadata[key] = value
		target.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	printJSON(target.Metadata)
}

func handleMetadataList(state *stateFile, args []string) {
	if len(args) < 4 {
		fatalf("issue metadata list requires an issue ID")
	}
	printJSON(requireIssue(state, args[3]).Metadata)
}

func handleCommentAdd(state *stateFile, args []string) {
	if len(args) < 4 {
		fatalf("issue comment add requires an issue ID")
	}
	issueID := args[3]
	requireIssue(state, issueID)
	cwd, _ := os.Getwd()
	body, ok := flagValue(args, "content-file")
	if !ok {
		fatalf("--content-file is required")
	}
	content := readWorkdirFile(cwd, body)
	state.NextComment++
	now := time.Now().UTC().Format(time.RFC3339)
	created := &comment{
		ID:        fmt.Sprintf("10000000-0000-4000-8000-%012d", state.NextComment),
		IssueID:   issueID,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}
	state.Comments[issueID] = append(state.Comments[issueID], created)
	printJSON(created)
}

func handleCommentList(state *stateFile, args []string) {
	if len(args) < 4 {
		fatalf("issue comment list requires an issue ID")
	}
	issueID := args[3]
	requireIssue(state, issueID)
	if os.Getenv("FAKE_MULTICA_OMIT_COMMENT") == "1" {
		printJSON([]*comment{})
		return
	}
	roots := make([]*comment, 0, len(state.Comments[issueID]))
	for _, existing := range state.Comments[issueID] {
		copied := *existing
		runes := []rune(copied.Content)
		if len(runes) > 80 {
			copied.Content = string(runes[:80])
			copied.ContentTruncated = true
		}
		roots = append(roots, &copied)
	}
	printJSON(roots)
}

func handleStatus(state *stateFile, args []string) {
	if len(args) < 4 {
		fatalf("issue status requires an issue ID and status")
	}
	target := requireIssue(state, args[2])
	status := args[3]
	if _, ok := flagValue(args, "no-start"); !ok {
		fatalf("issue status must pass --no-start")
	}
	switch status {
	case "backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled":
	default:
		fatalf("unsupported status %q", status)
	}
	if os.Getenv("FAKE_MULTICA_IGNORE_STATUS") != "1" {
		target.Status = status
		target.StatusCategory = status
		target.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	printJSON(target)
}

func requireIssue(state *stateFile, id string) *issue {
	target, ok := state.Issues[id]
	if !ok {
		fatalf("issue %s not found", id)
	}
	return target
}

func readWorkdirFile(cwd, rel string) string {
	if rel == "" || filepath.IsAbs(rel) {
		fatalf("file path must be relative to the working directory")
	}
	abs := filepath.Join(cwd, rel)
	relative, err := filepath.Rel(cwd, abs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		fatalf("file path must stay inside the working directory")
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		fatalf("file %s is not readable", rel)
	}
	if !utf8.Valid(content) {
		fatalf("file %s is not valid UTF-8", rel)
	}
	return string(content)
}

func flagValue(args []string, name string) (string, bool) {
	prefix := "--" + name + "="
	for i, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix), true
		}
		if arg == "--"+name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func failCommand(command string) bool {
	return strings.TrimSpace(os.Getenv("FAKE_MULTICA_FAIL")) == command
}

func maybeHang(command string) {
	if strings.TrimSpace(os.Getenv("FAKE_MULTICA_HANG")) != command {
		return
	}
	time.Sleep(30 * time.Second)
}

func loadState(path string) *stateFile {
	state := &stateFile{
		Issues:   map[string]*issue{},
		Comments: map[string][]*comment{},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(raw, state); err != nil {
		fatalf("fake state is invalid")
	}
	if state.Issues == nil {
		state.Issues = map[string]*issue{}
	}
	if state.Comments == nil {
		state.Comments = map[string][]*comment{}
	}
	return state
}

func saveState(path string, state *stateFile) {
	raw, err := json.Marshal(state)
	if err != nil {
		fatalf("fake state cannot be encoded")
	}
	// Test observers also read snapshots outside the CLI lock.
	temp, err := os.CreateTemp(filepath.Dir(path), ".fake-multica-*")
	if err != nil {
		fatalf("fake state temporary file cannot be created")
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		fatalf("fake state cannot be written")
	}
	if err := temp.Close(); err != nil {
		fatalf("fake state cannot be closed")
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		fatalf("fake state cannot be written")
	}
}

func recordInvocation(path string, value invocation) {
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fatalf("invocation log cannot be opened")
	}
	defer handle.Close()
	encoder := json.NewEncoder(handle)
	if err := encoder.Encode(value); err != nil {
		fatalf("invocation log cannot be written")
	}
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(value); err != nil {
		fatalf("result cannot be encoded")
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
