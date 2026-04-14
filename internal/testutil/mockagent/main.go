// Package main implements a minimal ACP mock agent used by integration tests.
// It performs the ACP handshake over stdin/stdout, handles one Prompt call,
// then exits when the connection closes.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

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
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo: &acp.Implementation{
			Name:    "mockagent",
			Version: "0.1.0",
		},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			McpCapabilities: acp.McpCapabilities{
				Sse: true,
			},
			PromptCapabilities: acp.PromptCapabilities{
				Image: true,
			},
		},
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
	if chunksRaw := os.Getenv("OAR_MOCKAGENT_CHUNKS"); chunksRaw != "" {
		chunks, err := strconv.Atoi(chunksRaw)
		if err != nil {
			return acp.PromptResponse{}, fmt.Errorf("invalid OAR_MOCKAGENT_CHUNKS: %w", err)
		}
		for i := 0; i < chunks; i++ {
			_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
				SessionId: p.SessionId,
				Update:    acp.UpdateAgentMessageText(fmt.Sprintf("text-chunk-%02d", i)),
			})
		}
		return acp.PromptResponse{
			StopReason: acp.StopReasonEndTurn,
		}, nil
	}

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

func (a *mockAgent) SetSessionConfigOption(_ context.Context, _ acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func main() {
	agent := &mockAgent{}
	conn := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.conn = conn
	<-conn.Done()
}
