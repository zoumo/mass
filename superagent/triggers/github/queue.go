package github

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/zoumo/mass/superagent/internal/triggercontent"
	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
)

// Convert a legacy head at delivery so the stored FIFO and retry state survive
// the cutover. A failed delivery leaves the original row available for retry.
func githubDeliveryPayload(payload []byte) ([]byte, error) {
	var legacy coreclient.Submission
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&legacy); err != nil {
		return nil, err
	}
	m := legacy.Message
	var threadKey string
	switch m.ThreadRef.Source {
	case "":
		return payload, nil
	case githubSource:
		threadKey = githubIssueThreadKey(m.ThreadRef.ID)
	case "pr:url":
		// The old GitHub adapter persisted its normalized PR URL here.
		threadKey = m.ThreadRef.ID
	default:
		return nil, errors.New("invalid queued GitHub thread source")
	}
	content, err := triggercontent.FromLegacyText(m.Text, m.Actor, m.At, m.URL)
	if err != nil {
		return nil, err
	}
	meta, err := githubDiagnostics(m.Meta, m.Human, m.SourceEventID, m.CollectorInstance)
	if err != nil {
		return nil, err
	}
	return json.Marshal(coreclient.TriggerSubmission{OperationID: legacy.OperationID, Message: coreclient.TriggerMessage{
		SourceMessageID: m.SourceMessageID, ThreadKey: threadKey, Content: content, Meta: meta,
	}})
}
