package events

import (
	"time"

	"github.com/zoumo/agent-sdk-go/agent"
)

type (
	MessagePayload          = agent.MessagePayload
	UserMessage             = agent.UserMessage
	AgentMessage            = agent.AgentMessage
	AgentThinking           = agent.AgentThinking
	MessageChunk            = agent.MessageChunk
	ThinkingChunk           = agent.ThinkingChunk
	ToolCall                = agent.ToolCallPayload
	ToolResult              = agent.ToolResultPayload
	ToolChunk               = agent.ToolChunkPayload
	ToolKind                = agent.ToolKind
	ToolProvider            = agent.ToolProvider
	Output                  = agent.ToolOutput
	Truncation              = agent.Truncation
	ObservedError           = agent.ObservedError
	StateUpdate             = agent.StateUpdatePayload
	SessionError            = agent.SessionErrorPayload
	InfoUpdate              = agent.InfoUpdatePayload
	ConfigOptionValue       = agent.ConfigOptionValue
	ConfigOptionGroup       = agent.ConfigOptionGroup
	ConfigOption            = agent.ConfigOption
	ConfigOptionUpdate      = agent.ConfigOptionUpdatePayload
	AvailableCommand        = agent.AvailableCommand
	AvailableCommandsUpdate = agent.AvailableCommandsUpdatePayload
	UsageCost               = agent.UsageCost
	UsageUpdate             = agent.UsageUpdatePayload
)

const (
	ToolKindRead       = agent.ToolKindRead
	ToolKindEdit       = agent.ToolKindEdit
	ToolKindDelete     = agent.ToolKindDelete
	ToolKindMove       = agent.ToolKindMove
	ToolKindSearch     = agent.ToolKindSearch
	ToolKindExecute    = agent.ToolKindExecute
	ToolKindThink      = agent.ToolKindThink
	ToolKindFetch      = agent.ToolKindFetch
	ToolKindSwitchMode = agent.ToolKindSwitchMode
	ToolKindOther      = agent.ToolKindOther

	ToolProviderAgent   = agent.ToolProviderAgent
	ToolProviderDynamic = agent.ToolProviderDynamic
	ToolProviderMCP     = agent.ToolProviderMCP
)

// ValidateConfigOption uses the SDK config payload validator rather than
// duplicating its select/grouped-select branches.
func ValidateConfigOption(option ConfigOption) error {
	return agent.Event{
		EventID:   "00000000-0000-4000-8000-000000000000",
		SessionID: "validation",
		Time:      time.Unix(1, 0).UTC(),
		Payload: agent.ConfigOptionUpdatePayload{
			ConfigOptions: []agent.ConfigOption{option},
		},
	}.Validate()
}

// ValidatePayload validates a live SDK payload using the SDK's closed union.
func ValidatePayload(payload Payload) error {
	live, ok := payload.(agent.Payload)
	if !ok {
		return &agent.Error{
			Code:    agent.ErrorInvalidArgument,
			Reason:  "unsupported_payload",
			Message: "event payload is not part of the SDK union",
		}
	}
	return agent.Event{
		EventID:   "00000000-0000-4000-8000-000000000000",
		SessionID: "validation",
		Time:      time.Unix(1, 0).UTC(),
		ItemID:    "validation",
		Payload:   live,
	}.Validate()
}
