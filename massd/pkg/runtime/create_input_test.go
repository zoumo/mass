package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
)

func TestCopyInputSnapshotsEverySDKUnionBranch(t *testing.T) {
	raw := `{"content":[{"type":"text","text":"hello","annotations":{"audience":["user"]}},{"type":"image","data":"aA==","mimeType":"image/png"},{"type":"audio","data":"aA==","mimeType":"audio/wav"},{"type":"resource_link","uri":"file:///link","name":"link"},{"type":"resource","resource":{"uri":"file:///embedded","text":"embedded"}}]}`
	var input Input
	require.NoError(t, json.Unmarshal([]byte(raw), &input))
	copied, err := CopyInput(input)
	require.NoError(t, err)
	input.Content[0].Text.Text = "changed"
	input.Content[0].Text.Annotations.Audience[0] = "assistant"
	input.Content[1].Image.Data = "Yg=="
	input.Content[2].Audio.MIMEType = "audio/mpeg"
	input.Content[3].ResourceLink.URI = "file:///changed"
	*input.Content[4].Resource.Resource.Text = "changed"
	encoded, err := json.Marshal(copied)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(encoded), "async input owns all nested union values")
}

func TestValidateTextInputRetainsSDKUnionValidation(t *testing.T) {
	for _, block := range []agent.ContentBlock{{}, {Text: &agent.ContentBlockText{Text: "text"}, Image: &agent.ContentBlockImage{Data: "aA==", MIMEType: "image/png"}}} {
		require.ErrorIs(t, ValidateTextInput(Input{Content: []agent.ContentBlock{block}}), ErrInvalidArgument)
	}
	for _, raw := range []string{
		`{"type":"text","text":"hello","annotations":{"audience":["user"]}}`,
		`{"type":"image","data":"aA==","mimeType":"image/png"}`,
		`{"type":"audio","data":"aA==","mimeType":"audio/wav"}`,
		`{"type":"resource_link","uri":"file:///link","name":"link"}`,
		`{"type":"resource","resource":{"uri":"file:///embedded","text":"embedded"}}`,
	} {
		var block agent.ContentBlock
		require.NoError(t, json.Unmarshal([]byte(raw), &block))
		err := ValidateTextInput(Input{Content: []agent.ContentBlock{block}})
		var public *OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, "unsupported", public.Code)
	}
	require.NoError(t, ValidateTextInput(Input{Content: []agent.ContentBlock{{Text: &agent.ContentBlockText{Text: "plain"}}}}))
}
