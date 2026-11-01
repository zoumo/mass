package codex

import (
	"encoding/json"
	"strconv"

	ct "github.com/zoumo/agent-sdk-go/codex/types"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

type commandApprovalChoice struct {
	decision ct.CommandExecutionApprovalDecision
	approve  bool
}

// Bind display choices to complete typed decisions, never reconstruct authority
// from a public option ID or the presentation-only permission kind.
func commandApprovalChoices(raw json.RawMessage) ([]events.PermissionOption, map[string]commandApprovalChoice, bool) {
	var params struct {
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return nil, nil, false
	}
	if params.AvailableDecisions == nil {
		params.AvailableDecisions = []json.RawMessage{json.RawMessage(`"accept"`), json.RawMessage(`"decline"`)}
	}
	options := make([]events.PermissionOption, 0, len(params.AvailableDecisions))
	choices := make(map[string]commandApprovalChoice, len(params.AvailableDecisions))
	seen := map[string]bool{}
	for i, raw := range params.AvailableDecisions {
		var decision ct.CommandExecutionApprovalDecision
		if json.Unmarshal(raw, &decision) != nil || decision.Unknown != nil {
			return nil, nil, false
		}
		canonical, err := json.Marshal(decision)
		if err != nil || seen[string(canonical)] {
			return nil, nil, false
		}
		seen[string(canonical)] = true
		option := events.PermissionOption{OptionID: "native-choice-" + strconv.Itoa(i)}
		approve := false
		switch {
		case decision.Value != nil:
			switch *decision.Value {
			case "accept":
				option = events.PermissionOption{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"}
				approve = true
			case "acceptForSession":
				option = events.PermissionOption{OptionID: "allow-session", Name: "Allow for this session", Kind: "allow_always"}
				approve = true
			case "decline":
				option = events.PermissionOption{OptionID: "reject-once", Name: "Reject once", Kind: "reject_once"}
			case "cancel":
				option = events.PermissionOption{OptionID: "cancel", Name: "Cancel", Kind: "reject_once"}
			default:
				return nil, nil, false
			}
		case decision.AcceptWithExecpolicyAmendment != nil:
			amendment := decision.AcceptWithExecpolicyAmendment
			if len(amendment.UnknownFields) != 0 || len(amendment.AcceptWithExecpolicyAmendment.UnknownFields) != 0 {
				return nil, nil, false
			}
			option.Name = "Allow and apply execpolicy amendment: " + string(canonical)
			option.Kind, approve = "allow_always", true
		case decision.ApplyNetworkPolicyAmendment != nil:
			amendment := decision.ApplyNetworkPolicyAmendment
			body := amendment.ApplyNetworkPolicyAmendment
			policy := body.NetworkPolicyAmendment
			if len(amendment.UnknownFields) != 0 || len(body.UnknownFields) != 0 || len(policy.UnknownFields) != 0 {
				return nil, nil, false
			}
			switch policy.Action {
			case ct.NetworkPolicyRuleActionAllow:
				option.Name, option.Kind, approve = "Apply persistent network allow rule: "+string(canonical), "allow_always", true
			case ct.NetworkPolicyRuleActionDeny:
				option.Name, option.Kind = "Apply persistent network deny rule: "+string(canonical), "reject_always"
			default:
				return nil, nil, false
			}
		default:
			return nil, nil, false
		}
		if _, exists := choices[option.OptionID]; exists {
			return nil, nil, false
		}
		options = append(options, option)
		choices[option.OptionID] = commandApprovalChoice{decision: decision, approve: approve}
	}
	return options, choices, len(choices) != 0
}
