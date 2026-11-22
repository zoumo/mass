package recordingtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/mass/internal/testpeer/replay"
)

func auditLegacyClaudeArgs(args []string, model string) error {
	awaitClaim, modelFlag := false, false
	for _, argument := range args {
		switch {
		case argument == "--await-claim":
			awaitClaim = true
		case argument == "--session-id" || strings.HasPrefix(argument, "--session-id="):
			return errors.New("claude capture must not select a session id")
		case strings.HasPrefix(argument, "--model="):
			if argument != "--model="+model {
				return fmt.Errorf("claude capture model argument is %q", argument)
			}
			modelFlag = true
		}
	}
	if !awaitClaim || !modelFlag {
		return errors.New("claude capture is missing the explicit claim or model argument")
	}
	return nil
}

// auditClaudeCapture uses the protocol as evidence for consumer captures.
// The native launcher arguments may be absent from the recording metadata.
func auditClaudeCapture(material, model, sessionID, project string) error {
	file, err := os.Open(filepath.Join(material, "raw", "raw.jsonl"))
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	requests := map[string]string{}
	hooks := map[string]bool{}
	initializeRequested, initializeAccepted, claimRequested := false, false, false
	claimed, input, identity, assistant, completed := false, false, false, false, false
	for {
		var frame struct {
			Direction string `json:"direction"`
			Wire      struct {
				Type      string `json:"type"`
				Subtype   string `json:"subtype"`
				SessionID string `json:"session_id"`
				Model     string `json:"model"`
				CWD       string `json:"cwd"`
				IsError   bool   `json:"is_error"`
				HookID    string `json:"hook_id"`
				HookName  string `json:"hook_name"`
				HookEvent string `json:"hook_event"`
				Output    string `json:"output"`
				Stdout    string `json:"stdout"`
				Stderr    string `json:"stderr"`
				ExitCode  int    `json:"exit_code"`
				Outcome   string `json:"outcome"`
				RequestID string `json:"request_id"`
				Request   struct {
					Subtype   string          `json:"subtype"`
					CWD       string          `json:"cwd"`
					SessionID json.RawMessage `json:"session_id"`
				} `json:"request"`
				Response struct {
					Subtype   string `json:"subtype"`
					RequestID string `json:"request_id"`
					Result    struct {
						Status    string `json:"status"`
						SessionID string `json:"session_id"`
						CWD       string `json:"cwd"`
					} `json:"response"`
				} `json:"response"`
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			} `json:"wire"`
		}
		if err := decoder.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		wire := frame.Wire
		if frame.Direction == "c2s" {
			switch wire.Type {
			case "control_request":
				if wire.RequestID == "" || requests[wire.RequestID] != "" {
					return errors.New("claude control request has a missing or repeated identity")
				}
				switch wire.Request.Subtype {
				case "initialize":
					if initializeRequested || claimed || input || identity {
						return errors.New("claude initialize request is out of order")
					}
					initializeRequested = true
				case "claim_session":
					if !initializeAccepted || claimRequested || wire.SessionID != "" ||
						len(wire.Request.SessionID) != 0 || !sameNativePath(wire.Request.CWD, project) {
						return errors.New("claude claim_session request is out of order or carries a session identity")
					}
					claimRequested = true
				}
				requests[wire.RequestID] = wire.Request.Subtype
			case "user":
				if !initializeAccepted || !claimed {
					return errors.New("claude input precedes the initialize/claim handshake")
				}
				input = true
			}
		}
		if frame.Direction != "s2c" {
			continue
		}
		if wire.Type == "control_response" {
			subtype, ok := requests[wire.Response.RequestID]
			if !ok || wire.Response.Subtype != "success" {
				return errors.New("claude control response does not close a requested handshake")
			}
			switch subtype {
			case "initialize":
				initializeAccepted = true
			case "claim_session":
				if claimed || wire.Response.Result.Status != "ok" ||
					wire.Response.Result.SessionID == "" || wire.Response.Result.SessionID != sessionID ||
					!sameNativePath(wire.Response.Result.CWD, project) {
					return errors.New("claude claim_session did not confirm the captured session and project")
				}
				claimed = true
			}
			delete(requests, wire.Response.RequestID)
		}
		if wire.Type == "system" && (wire.Subtype == "hook_started" || wire.Subtype == "hook_response") {
			if wire.SessionID != sessionID || wire.HookID == "" || wire.HookName != "SessionStart:startup" || wire.HookEvent != "SessionStart" || wire.Output != "" || wire.Stdout != "" || wire.Stderr != "" {
				return errors.New("claude capture contains an unexpected startup hook")
			}
			if wire.Subtype == "hook_started" {
				if hooks[wire.HookID] {
					return errors.New("claude startup hook identity is repeated")
				}
				hooks[wire.HookID] = true
			} else {
				if !hooks[wire.HookID] || wire.ExitCode != 0 || wire.Outcome != "success" {
					return errors.New("claude startup hook did not close successfully")
				}
				delete(hooks, wire.HookID)
			}
		}
		if wire.Type == "system" && wire.Subtype == "init" {
			if !claimed || !input {
				return errors.New("claude initialization precedes its claim handshake or initial input")
			}
			if wire.SessionID != sessionID || wire.Model != model || !sameNativePath(wire.CWD, project) {
				return fmt.Errorf("native Claude init identity differs: %q/%q/%q", wire.SessionID, wire.Model, wire.CWD)
			}
			identity = true
		}
		if wire.Type == "assistant" {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(wire.Message.Content, &content); err != nil {
				return err
			}
			for _, block := range content {
				assistant = assistant || wire.SessionID == sessionID && block.Type == "text" && block.Text != ""
			}
		}
		if wire.Type == "result" {
			if !identity || !assistant || wire.SessionID != sessionID || wire.Subtype != "success" || wire.IsError {
				return errors.New("native Claude result did not succeed for the captured session")
			}
			completed = true
		}
	}
	if !initializeRequested || !initializeAccepted || !claimed || !input || !identity || !assistant || !completed || len(hooks) != 0 || len(requests) != 0 {
		return errors.New("claude claim handshake, native init, assistant message or successful completion is missing")
	}
	return nil
}

func sameNativePath(left, right string) bool {
	return nativePathKey(left) == nativePathKey(right)
}

func nativePathKey(value string) string {
	if value == "" {
		return ""
	}
	cleaned := filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	for _, prefix := range []string{"/private/var", "/private/tmp"} {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return strings.TrimPrefix(cleaned, "/private")
		}
	}
	return cleaned
}

const claudeReplayReceipt = "claude-replay-complete"

func capturedClaudeUserUUID(material string, legacy bool) (string, error) {
	file, err := os.Open(filepath.Join(material, "raw", "raw.jsonl"))
	if err != nil {
		return "", err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var uuid string
	userSeen := false
	for {
		var frame struct {
			Direction string `json:"direction"`
			Channel   string `json:"channel"`
			Wire      struct {
				Type string          `json:"type"`
				UUID json.RawMessage `json:"uuid"`
			} `json:"wire"`
		}
		if err := decoder.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		if frame.Direction != "c2s" || frame.Channel != "protocol" || frame.Wire.Type != "user" {
			continue
		}
		if userSeen {
			return "", errors.New("claude capture has more than one user input")
		}
		userSeen = true
		if legacy {
			if len(frame.Wire.UUID) != 0 {
				return "", errors.New("legacy Claude capture unexpectedly has a user UUID")
			}
		} else if json.Unmarshal(frame.Wire.UUID, &uuid) != nil || !replay.ValidClaudeUserUUID(uuid) {
			return "", errors.New("claude capture requires a canonical v4 user UUID")
		}
	}
	if !userSeen {
		return "", errors.New("claude capture has no user input")
	}
	return uuid, nil
}
