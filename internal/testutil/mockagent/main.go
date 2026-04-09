// Package main implements a minimal ACP mock agent used by integration tests.
// It performs the ACP handshake over stdin/stdout, handles one Prompt call,
// then exits when the connection closes.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/coder/acp-go-sdk"
)

type mockAgent struct {
	conn *acp.AgentSideConnection
}

var _ acp.Agent = (*mockAgent)(nil)

func (a *mockAgent) Authenticate(_ context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *mockAgent) Initialize(_ context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{},
	}, nil
}

func (a *mockAgent) Cancel(_ context.Context, _ acp.CancelNotification) error {
	return nil
}

func (a *mockAgent) NewSession(_ context.Context, _ acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{
		SessionId: acp.SessionId("mock-session-" + fmt.Sprintf("%d", os.Getpid())),
	}, nil
}

func (a *mockAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	// Attempt a WriteTextFile to exercise the client-side permission policy.
	// The result (allowed or denied) is surfaced as a session update so the
	// integration test can observe which policy was in effect.
	_, writeErr := a.conn.WriteTextFile(ctx, acp.WriteTextFileRequest{
		Path:    "/tmp/mock-agent-test.txt",
		Content: "hello from mock agent",
	})
	notifText := "write:ok"
	if writeErr != nil {
		notifText = "write:denied:" + writeErr.Error()
	}
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: p.SessionId,
		Update:    acp.UpdateAgentMessageText(notifText),
	})
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: p.SessionId,
		Update:    acp.UpdateAgentMessageText("mock response"),
	})
	return acp.PromptResponse{
		StopReason: acp.StopReasonEndTurn,
	}, nil
}

func (a *mockAgent) SetSessionMode(_ context.Context, _ acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func main() {
	agent := &mockAgent{}
	conn := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.conn = conn
	<-conn.Done()
}
