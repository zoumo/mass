// Package main provides session management commands for the agentdctl CLI.
// Session commands allow creating, listing, prompting, stopping, and removing sessions.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/spf13/cobra"
)

// sessionCmd is the root command for session management operations.
var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Session management commands",
}

// sessionNewCmd creates a new session with the specified workspace and runtime class.
var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new session",
	RunE:  runSessionNew,
}

// sessionListCmd lists all sessions in the registry.
var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sessions",
	RunE:  runSessionList,
}

// sessionStatusCmd gets detailed status for a specific session.
var sessionStatusCmd = &cobra.Command{
	Use:   "status <session-id>",
	Short: "Get session status",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionStatus,
}

// sessionPromptCmd sends a prompt to a running session.
var sessionPromptCmd = &cobra.Command{
	Use:   "prompt <session-id>",
	Short: "Send prompt to session",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionPrompt,
}

// sessionStopCmd stops a running session.
var sessionStopCmd = &cobra.Command{
	Use:   "stop <session-id>",
	Short: "Stop a session",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionStop,
}

// sessionRemoveCmd removes a session from the registry.
var sessionRemoveCmd = &cobra.Command{
	Use:   "remove <session-id>",
	Short: "Remove a session",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionRemove,
}

// sessionAttachCmd gets the shim socket path for attaching to a session.
var sessionAttachCmd = &cobra.Command{
	Use:   "attach <session-id>",
	Short: "Get shim socket path for attaching",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionAttach,
}

// Flags for session new command.
var (
	sessionNewWorkspaceId   string
	sessionNewRuntimeClass  string
	sessionNewLabels        string // comma-separated key=value pairs
	sessionNewRoom          string
	sessionNewRoomAgent     string
)

// Flags for session prompt command.
var sessionPromptText string

func init() {
	// Add flags to sessionNewCmd
	sessionNewCmd.Flags().StringVar(&sessionNewWorkspaceId, "workspace-id", "", "Workspace ID (required)")
	sessionNewCmd.Flags().StringVar(&sessionNewRuntimeClass, "runtime-class", "", "Runtime class (required)")
	sessionNewCmd.Flags().StringVar(&sessionNewLabels, "labels", "", "Labels as comma-separated key=value pairs")
	sessionNewCmd.Flags().StringVar(&sessionNewRoom, "room", "", "Room name for multi-agent coordination")
	sessionNewCmd.Flags().StringVar(&sessionNewRoomAgent, "room-agent", "", "Agent name/ID within room")
	_ = sessionNewCmd.MarkFlagRequired("workspace-id")
	_ = sessionNewCmd.MarkFlagRequired("runtime-class")

	// Add flags to sessionPromptCmd
	sessionPromptCmd.Flags().StringVar(&sessionPromptText, "text", "", "Prompt text (required)")
	_ = sessionPromptCmd.MarkFlagRequired("text")

	// Add subcommands to sessionCmd
	sessionCmd.AddCommand(sessionNewCmd)
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionStatusCmd)
	sessionCmd.AddCommand(sessionPromptCmd)
	sessionCmd.AddCommand(sessionStopCmd)
	sessionCmd.AddCommand(sessionRemoveCmd)
	sessionCmd.AddCommand(sessionAttachCmd)
}

// runSessionNew creates a new session via the ARI session/new method.
func runSessionNew(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.SessionNewParams{
		WorkspaceId:  sessionNewWorkspaceId,
		RuntimeClass: sessionNewRuntimeClass,
	}

	// Parse labels if provided
	if sessionNewLabels != "" {
		params.Labels = parseLabels(sessionNewLabels)
	}

	// Add room settings if provided
	if sessionNewRoom != "" {
		params.Room = sessionNewRoom
	}
	if sessionNewRoomAgent != "" {
		params.RoomAgent = sessionNewRoomAgent
	}

	var result ari.SessionNewResult
	if err := client.Call("session/new", params, &result); err != nil {
		handleError(err)
		return nil // handleError calls os.Exit, but return nil for cobra
	}

	outputJSON(result)
	return nil
}

// runSessionList lists all sessions via the ARI session/list method.
func runSessionList(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.SessionListParams{}
	var result ari.SessionListResult
	if err := client.Call("session/list", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runSessionStatus gets status for a specific session.
func runSessionStatus(cmd *cobra.Command, args []string) error {
	sessionId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.SessionStatusParams{SessionId: sessionId}
	var result ari.SessionStatusResult
	if err := client.Call("session/status", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runSessionPrompt sends a prompt to a session.
func runSessionPrompt(cmd *cobra.Command, args []string) error {
	sessionId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.SessionPromptParams{
		SessionId: sessionId,
		Text:      sessionPromptText,
	}
	var result ari.SessionPromptResult
	if err := client.Call("session/prompt", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runSessionStop stops a running session.
func runSessionStop(cmd *cobra.Command, args []string) error {
	sessionId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.SessionStopParams{SessionId: sessionId}
	if err := client.Call("session/stop", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Session %s stopped\n", sessionId)
	return nil
}

// runSessionRemove removes a session from the registry.
func runSessionRemove(cmd *cobra.Command, args []string) error {
	sessionId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.SessionRemoveParams{SessionId: sessionId}
	if err := client.Call("session/remove", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Session %s removed\n", sessionId)
	return nil
}

// runSessionAttach gets the shim socket path for attaching to a session.
func runSessionAttach(cmd *cobra.Command, args []string) error {
	sessionId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.SessionAttachParams{SessionId: sessionId}
	var result ari.SessionAttachResult
	if err := client.Call("session/attach", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// getClient creates an ARI client connected to the socket path.
// The socketPath is set by the root command's persistent flag.
func getClient() (*ari.Client, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("socket path not set")
	}
	return ari.NewClient(socketPath)
}

// outputJSON pretty-prints the result as JSON to stdout.
func outputJSON(result any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// handleError prints the error to stderr and exits with code 1.
func handleError(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}

// parseLabels parses comma-separated key=value pairs into a map.
// Example: "env=dev,team=infra" -> {"env": "dev", "team": "infra"}
func parseLabels(labelsStr string) map[string]string {
	labels := make(map[string]string)
	if labelsStr == "" {
		return labels
	}

	pairs := splitComma(labelsStr)
	for _, pair := range pairs {
		key, value, ok := splitKeyValue(pair)
		if ok {
			labels[key] = value
		}
	}
	return labels
}

// splitComma splits a string by comma, trimming whitespace.
func splitComma(s string) []string {
	var result []string
	for _, part := range splitBy(s, ',') {
		result = append(result, trimSpace(part))
	}
	return result
}

// splitKeyValue splits a string by '=' into key and value.
func splitKeyValue(s string) (key, value string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			key = trimSpace(s[:i])
			value = trimSpace(s[i+1:])
			return key, value, key != "" && value != ""
		}
	}
	return "", "", false
}

// splitBy splits a string by a delimiter character.
func splitBy(s string, delim byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == delim {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

// trimSpace trims whitespace from both ends of a string.
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && isWhitespace(s[start]) {
		start++
	}
	for end > start && isWhitespace(s[end-1]) {
		end--
	}
	return s[start:end]
}

// isWhitespace checks if a byte is whitespace.
func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}