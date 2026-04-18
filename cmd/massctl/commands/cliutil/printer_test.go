package cliutil

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testItem struct {
	Name  string `json:"name" yaml:"name"`
	State string `json:"state" yaml:"state"`
	Extra string `json:"extra" yaml:"extra"`
}

func testColumns() []Column {
	return []Column{
		{Header: "NAME", Field: func(v any) string { return v.(testItem).Name }},
		{Header: "STATE", Field: func(v any) string { return v.(testItem).State }},
		{Header: "EXTRA", Field: func(v any) string { return v.(testItem).Extra }, Wide: true},
	}
}

func TestPrintTable(t *testing.T) {
	p := &ResourcePrinter{Format: FormatTable, Columns: testColumns()}
	items := []any{
		testItem{Name: "foo", State: "running", Extra: "x"},
		testItem{Name: "bar", State: "idle", Extra: "y"},
	}
	var buf bytes.Buffer
	require.NoError(t, p.Print(&buf, items))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	assert.Len(t, lines, 3) // header + 2 rows
	assert.Contains(t, lines[0], "NAME")
	assert.Contains(t, lines[0], "STATE")
	assert.NotContains(t, lines[0], "EXTRA") // hidden in table mode
	assert.Contains(t, lines[1], "foo")
	assert.Contains(t, lines[2], "bar")
}

func TestPrintWide(t *testing.T) {
	p := &ResourcePrinter{Format: FormatWide, Columns: testColumns()}
	items := []any{testItem{Name: "foo", State: "running", Extra: "detail"}}
	var buf bytes.Buffer
	require.NoError(t, p.Print(&buf, items))

	assert.Contains(t, buf.String(), "EXTRA") // visible in wide mode
	assert.Contains(t, buf.String(), "detail")
}

func TestPrintJSON_Single(t *testing.T) {
	p := &ResourcePrinter{Format: FormatJSON}
	items := []any{testItem{Name: "foo", State: "ok"}}
	var buf bytes.Buffer
	require.NoError(t, p.Print(&buf, items))

	// Single item → not wrapped in array.
	assert.Contains(t, buf.String(), `"name": "foo"`)
	assert.NotContains(t, buf.String(), "[")
}

func TestPrintJSON_Multiple(t *testing.T) {
	p := &ResourcePrinter{Format: FormatJSON}
	items := []any{
		testItem{Name: "a"},
		testItem{Name: "b"},
	}
	var buf bytes.Buffer
	require.NoError(t, p.Print(&buf, items))

	// Multiple items → JSON array.
	assert.Contains(t, buf.String(), "[")
}

func TestPrintYAML(t *testing.T) {
	p := &ResourcePrinter{Format: FormatYAML}
	items := []any{testItem{Name: "foo", State: "ok"}}
	var buf bytes.Buffer
	require.NoError(t, p.Print(&buf, items))

	assert.Contains(t, buf.String(), "name: foo")
	assert.Contains(t, buf.String(), "state: ok")
}

func TestPrintUnknownFormat(t *testing.T) {
	p := &ResourcePrinter{Format: "csv"}
	err := p.Print(&bytes.Buffer{}, []any{testItem{}})
	assert.ErrorContains(t, err, "unknown output format")
}
