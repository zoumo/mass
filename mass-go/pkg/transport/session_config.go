package transport

import (
	"context"
	"encoding/json"

	"github.com/danielgtaylor/huma/v2"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
)

// Configuration DTOs only map the SDK's Go names to the public wire spelling.
type configChange struct {
	ID    agent.ConfigID `json:"id" enum:"model,effort"`
	Value string         `json:"value" minLength:"1"`
}

func (c *configChange) sdk() runtime.ConfigChange {
	return runtime.ConfigChange{ID: c.ID, Value: c.Value}
}

func (c *configChange) UnmarshalJSON(raw []byte) error {
	type plain configChange
	var next plain
	if err := json.Unmarshal(raw, &next); err != nil {
		return Invalid()
	}
	// Reject Unicode escapes that encoding/json would silently replace.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Invalid()
	}
	for _, value := range fields {
		if len(value) == 0 || value[0] != '"' || !runtime.ValidConfigStringJSON(value) {
			return Invalid()
		}
	}
	change := configChange(next)
	if err := change.sdk().Validate(); err != nil {
		return agentrelay.MapError(err)
	}
	*c = change
	return nil
}

type configReceipt struct {
	Change     configChange       `json:"change"`
	Acceptance runtime.Acceptance `json:"acceptance"`
}
type (
	configOutput struct{ Body configReceipt }
	configByID   struct {
		SessionIDPath
		Body configChange
	}
	configByNameOrID struct {
		SessionNameOrIDPath
		Body configChange
	}
)

func configResult(receipt runtime.ConfigReceipt, err error) (*configOutput, error) {
	return &configOutput{Body: configReceipt{Change: configChange{ID: receipt.Change.ID, Value: receipt.Change.Value}, Acceptance: agentrelay.MapAcceptance(receipt.Acceptance)}}, err
}

func (s *Server) registerSessionConfig(api huma.API) {
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/sessions/{sessionId}/config", OperationID: "setSessionConfigByID"}, func(ctx context.Context, in *configByID) (*configOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return configResult(s.api.SetSessionConfig(ctx, ref, in.Body.sdk()))
	})
	register(api, huma.Operation{Method: "POST", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/config", OperationID: "setSessionConfigByNameOrID"}, func(ctx context.Context, in *configByNameOrID) (*configOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return configResult(s.api.SetSessionConfig(ctx, ref, in.Body.sdk()))
	})
}
