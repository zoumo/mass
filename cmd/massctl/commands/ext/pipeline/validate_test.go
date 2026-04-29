package pipeline

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCmd_ValidExample(t *testing.T) {
	cmd := newValidateCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{filepath.Join("testdata", "example-pipeline.yaml")})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, out.String(), "validated successfully")
}

func TestValidateCmd_MissingName(t *testing.T) {
	cmd := newValidateCmd()
	cmd.SetOut(&bytes.Buffer{})
	errBuf := &bytes.Buffer{}
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{filepath.Join("testdata", "invalid-missing-name.yaml")})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema validation failed")
}

func TestValidateCmd_InvalidGoto(t *testing.T) {
	cmd := newValidateCmd()
	cmd.SetOut(&bytes.Buffer{})
	errBuf := &bytes.Buffer{}
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{filepath.Join("testdata", "invalid-goto.yaml")})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, errBuf.String(), "nowhere")
}

func TestValidateCmd_OrphanStage(t *testing.T) {
	cmd := newValidateCmd()
	cmd.SetOut(&bytes.Buffer{})
	errBuf := &bytes.Buffer{}
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{filepath.Join("testdata", "invalid-orphan.yaml")})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, errBuf.String(), "unreachable")
}

func TestValidateCmd_DuplicateStage(t *testing.T) {
	cmd := newValidateCmd()
	cmd.SetOut(&bytes.Buffer{})
	errBuf := &bytes.Buffer{}
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{filepath.Join("testdata", "invalid-duplicate-stage.yaml")})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, errBuf.String(), "duplicate stage name")
}

func TestValidateCmd_FileNotFound(t *testing.T) {
	cmd := newValidateCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"nonexistent.yaml"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading")
}

func TestExampleCmd_Stdout(t *testing.T) {
	cmd := newExampleCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs(nil)

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, out.String(), "name: my-pipeline")
	assert.Contains(t, out.String(), "stages:")
}

func TestExampleCmd_File(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "test-pipeline.yaml")
	cmd := newExampleCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"-o", outPath})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Wrote example pipeline")

	// Validate the generated file
	validateCmd := newValidateCmd()
	validateOut := &bytes.Buffer{}
	validateCmd.SetOut(validateOut)
	validateCmd.SetErr(&bytes.Buffer{})
	validateCmd.SetArgs([]string{outPath})

	err = validateCmd.Execute()
	require.NoError(t, err, "generated example should pass validation")
}
