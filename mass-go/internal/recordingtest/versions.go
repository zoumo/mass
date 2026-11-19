package recordingtest

import "github.com/zoumo/mass/mass-go/internal/testpeer"

const (
	// RuntimeSDKVersion is written into newly recorded consumer fixtures.
	RuntimeSDKVersion = testpeer.SDKVersion
	// ClaudeRecordingSDKVersion is the immutable provenance of the active Claude fixtures.
	ClaudeRecordingSDKVersion = "v0.0.0-20261113030733-09d4ddf1ff20"
	// RecordingSDKVersion is the immutable provenance of checked-in fixtures.
	RecordingSDKVersion = "v0.0.0-20261105065348-5edf8b62e963"
	// TestifyVersion is written into newly recorded consumer fixtures.
	TestifyVersion = testpeer.TestifyVersion
	// ClaudeRecordingTestifyVersion identifies the tool that captured the active Claude fixtures.
	ClaudeRecordingTestifyVersion = "v0.2.1"
	// RecordingTestifyVersion is the immutable provenance of checked-in fixtures.
	RecordingTestifyVersion = "v0.0.0-20261105165039-e4799fe3484c"
)

// testifyExecutionIndexSchemaVersion is the execution index layout written by
// the pinned agent-sdk-testify preparation.
const testifyExecutionIndexSchemaVersion = 7
