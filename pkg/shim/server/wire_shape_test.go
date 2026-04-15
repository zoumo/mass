package server

import (
	"encoding/json"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apishim "github.com/zoumo/mass/pkg/shim/api"
)

// jsonKeys returns the top-level keys of a JSON object, ignoring sessionUpdate.
func jsonKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &m))
	keys := make(map[string]bool, len(m))
	for k := range m {
		if k == "sessionUpdate" {
			continue // omit the ACP wire discriminator per design
		}
		keys[k] = true
	}
	return keys
}

// ContentBlock wire shape tests are removed — ContentBlock is now a direct
// type alias of acp.ContentBlock, so wire format identity is guaranteed.

// EmbeddedResource wire shape tests are removed — same reason.

// ── Test 17: ToolCallContent JSON shape alignment ─────────────────────────────

func TestWireShape_ToolCallContent_Content(t *testing.T) {
	acpObj := acp.ToolCallContent{Content: &acp.ToolCallContentContent{
		Content: acp.TextBlock("hi"),
	}}
	shimObj := apishim.ToolCallContent{Content: &apishim.ToolCallContentContent{
		Content: apishim.TextBlock("hi"),
	}}
	assert.Equal(t, jsonKeys(t, acpObj), jsonKeys(t, shimObj), "ToolCallContent content variant")
}

func TestWireShape_ToolCallContent_Diff(t *testing.T) {
	acpObj := acp.ToolCallContent{Diff: &acp.ToolCallContentDiff{Path: "a.go", NewText: "x"}}
	shimObj := apishim.ToolCallContent{Diff: &apishim.ToolCallContentDiff{Path: "a.go", NewText: "x"}}
	assert.Equal(t, jsonKeys(t, acpObj), jsonKeys(t, shimObj), "ToolCallContent diff variant")
}

func TestWireShape_ToolCallContent_Terminal(t *testing.T) {
	acpObj := acp.ToolCallContent{Terminal: &acp.ToolCallContentTerminal{TerminalId: "t1"}}
	shimObj := apishim.ToolCallContent{Terminal: &apishim.ToolCallContentTerminal{TerminalID: "t1"}}
	assert.Equal(t, jsonKeys(t, acpObj), jsonKeys(t, shimObj), "ToolCallContent terminal variant")
}

// ── Test 18: AvailableCommandInput JSON shape alignment ───────────────────────

func TestWireShape_AvailableCommandInput_Unstructured(t *testing.T) {
	acpObj := acp.AvailableCommandInput{
		Unstructured: &acp.UnstructuredCommandInput{Hint: "flags here"},
	}
	shimObj := apishim.AvailableCommandInput{
		Unstructured: &apishim.UnstructuredCommandInput{Hint: "flags here"},
	}
	acpKeys := jsonKeys(t, acpObj)
	oarKeys := jsonKeys(t, shimObj)
	assert.Equal(t, acpKeys, oarKeys, "AvailableCommandInput unstructured variant")
	// Must be flat {"hint":"..."} — no type field, no nesting.
	assert.True(t, acpKeys["hint"], "hint field must be present")
	assert.False(t, acpKeys["type"], "type field must NOT be present (no discriminator)")
	assert.False(t, acpKeys["unstructured"], "no nesting wrapper")
}

// ── Test 19: ConfigOption JSON shape alignment ────────────────────────────────

func TestWireShape_ConfigOption_Select_Ungrouped(t *testing.T) {
	ungrouped := acp.SessionConfigSelectOptionsUngrouped([]acp.SessionConfigSelectOption{
		{Name: "fast", Value: "fast"},
	})
	acpObj := acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: "model", Name: "Model", CurrentValue: "fast",
		Options: acp.SessionConfigSelectOptions{Ungrouped: &ungrouped},
	}}
	shimObj := apishim.ConfigOption{Select: &apishim.ConfigOptionSelect{
		ID: "model", Name: "Model", CurrentValue: "fast",
		Options: apishim.ConfigSelectOptions{Ungrouped: []apishim.ConfigSelectOption{{Name: "fast", Value: "fast"}}},
	}}

	acpKeys := jsonKeys(t, acpObj)
	oarKeys := jsonKeys(t, shimObj)
	assert.Equal(t, acpKeys, oarKeys, "ConfigOption select variant top-level keys")
	assert.True(t, acpKeys["type"], "type discriminator must be present")
	assert.True(t, acpKeys["id"])
	assert.True(t, acpKeys["options"])

	// Check options wire shape: must be bare array.
	acpOptJSON, _ := json.Marshal(acpObj)
	var acpMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(acpOptJSON, &acpMap))
	var acpOpts []json.RawMessage
	require.NoError(t, json.Unmarshal(acpMap["options"], &acpOpts), "options must be bare array")

	oarOptJSON, _ := json.Marshal(shimObj)
	var oarMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(oarOptJSON, &oarMap))
	var oarOpts []json.RawMessage
	require.NoError(t, json.Unmarshal(oarMap["options"], &oarOpts), "options must be bare array")
}

func TestWireShape_ConfigOption_Select_Grouped(t *testing.T) {
	grouped := acp.SessionConfigSelectOptionsGrouped([]acp.SessionConfigSelectGroup{
		{Group: "g1", Name: "Group 1", Options: []acp.SessionConfigSelectOption{{Name: "opt", Value: "opt"}}},
	})
	acpObj := acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: "model", Name: "Model", CurrentValue: "opt",
		Options: acp.SessionConfigSelectOptions{Grouped: &grouped},
	}}
	shimObj := apishim.ConfigOption{Select: &apishim.ConfigOptionSelect{
		ID: "model", Name: "Model", CurrentValue: "opt",
		Options: apishim.ConfigSelectOptions{Grouped: []apishim.ConfigSelectGroup{
			{Group: "g1", Name: "Group 1", Options: []apishim.ConfigSelectOption{{Name: "opt", Value: "opt"}}},
		}},
	}}

	acpKeys := jsonKeys(t, acpObj)
	oarKeys := jsonKeys(t, shimObj)
	assert.Equal(t, acpKeys, oarKeys, "ConfigOption select+grouped top-level keys")

	// options must be bare array of group objects.
	acpOptJSON, _ := json.Marshal(acpObj)
	var acpMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(acpOptJSON, &acpMap))
	var acpOpts []json.RawMessage
	require.NoError(t, json.Unmarshal(acpMap["options"], &acpOpts))
	var grp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(acpOpts[0], &grp))
	assert.Contains(t, grp, "group")
	assert.Contains(t, grp, "options")
}

// ── Test 21: Union empty variant marshal errors ─────────────────────────────
// ContentBlock and EmbeddedResource are ACP aliases — ACP does not error on
// empty unions (returns empty bytes). Only our custom union types are tested.

func TestWireShape_UnionEmptyVariant_MarshalErrors(t *testing.T) {
	t.Run("ToolCallContent empty", func(t *testing.T) {
		_, err := json.Marshal(apishim.ToolCallContent{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ToolCallContent")
	})
	t.Run("ConfigOption empty", func(t *testing.T) {
		_, err := json.Marshal(apishim.ConfigOption{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ConfigOption")
	})
	t.Run("AvailableCommandInput empty", func(t *testing.T) {
		_, err := json.Marshal(apishim.AvailableCommandInput{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AvailableCommandInput")
	})
}

// ── Test 21b: Union multi-variant marshal errors ─────────────────────────────

func TestWireShape_UnionMultiVariant_MarshalErrors(t *testing.T) {
	t.Run("ToolCallContent multi-variant", func(t *testing.T) {
		tc := apishim.ToolCallContent{
			Diff:     &apishim.ToolCallContentDiff{Path: "a.go", NewText: "x"},
			Terminal: &apishim.ToolCallContentTerminal{TerminalID: "t1"},
		}
		_, err := json.Marshal(tc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ToolCallContent")
		assert.Contains(t, err.Error(), "multiple")
	})
	t.Run("ConfigSelectOptions multi-variant", func(t *testing.T) {
		cso := apishim.ConfigSelectOptions{
			Ungrouped: []apishim.ConfigSelectOption{{Name: "a", Value: "a"}},
			Grouped:   []apishim.ConfigSelectGroup{{Group: "g", Name: "G", Options: nil}},
		}
		_, err := json.Marshal(cso)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ConfigSelectOptions")
		assert.Contains(t, err.Error(), "multiple")
	})
}

// ── Test 21c: ConfigSelectOptions empty marshal error ────────────────────────

func TestWireShape_ConfigSelectOptions_EmptyMarshalError(t *testing.T) {
	_, err := json.Marshal(apishim.ConfigSelectOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ConfigSelectOptions")
}

// ── Test 22: Union unknown type unmarshal errors ──────────────────────────────
// ContentBlock and EmbeddedResource unmarshal tests removed — ACP's generated
// UnmarshalJSON uses fallback brute-force matching, so "unknown type" does not
// always produce an error. Only our custom union types are tested.

func TestWireShape_UnionUnknownType_UnmarshalErrors(t *testing.T) {
	t.Run("ToolCallContent unknown type", func(t *testing.T) {
		var tc apishim.ToolCallContent
		err := json.Unmarshal([]byte(`{"type":"unknown_variant"}`), &tc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ToolCallContent")
	})
	t.Run("ConfigOption unknown type", func(t *testing.T) {
		var co apishim.ConfigOption
		err := json.Unmarshal([]byte(`{"type":"checkbox","id":"x"}`), &co)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ConfigOption")
		assert.Contains(t, err.Error(), "checkbox")
	})
	t.Run("AvailableCommandInput no matching variant", func(t *testing.T) {
		var ai apishim.AvailableCommandInput
		err := json.Unmarshal([]byte(`{"unknown":"x"}`), &ai)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AvailableCommandInput")
	})
	t.Run("ConfigSelectOptions empty array", func(t *testing.T) {
		var cso apishim.ConfigSelectOptions
		err := json.Unmarshal([]byte(`[]`), &cso)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ConfigSelectOptions")
	})
	t.Run("ConfigSelectOptions no matching element shape", func(t *testing.T) {
		var cso apishim.ConfigSelectOptions
		err := json.Unmarshal([]byte(`[{"unknown":"x"}]`), &cso)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ConfigSelectOptions")
	})
}
