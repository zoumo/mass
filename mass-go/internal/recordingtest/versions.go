package recordingtest

const (
	// RuntimeSDKVersion is written into newly recorded consumer fixtures.
	RuntimeSDKVersion = "v0.0.0-20261113030733-09d4ddf1ff20"
	// RecordingSDKVersion is the immutable provenance of checked-in fixtures.
	RecordingSDKVersion = "v0.0.0-20261105065348-5edf8b62e963"
	// TestifyVersion is written into newly recorded consumer fixtures.
	TestifyVersion = "v0.1.3-0.20261113014718-45bce662f756"
	// RecordingTestifyVersion is the immutable provenance of checked-in fixtures.
	RecordingTestifyVersion = "v0.0.0-20261105165039-e4799fe3484c"
)

// testifyExecutionIndexSchemaVersion is the execution index layout written by
// the pinned agent-sdk-testify preparation.
const testifyExecutionIndexSchemaVersion = 7
