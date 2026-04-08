module github.com/open-agent-d/open-agent-d

go 1.24.13

require (
	github.com/coder/acp-go-sdk v0.6.3
	github.com/google/uuid v1.6.0
	github.com/mattn/go-sqlite3 v1.14.38
	github.com/sourcegraph/jsonrpc2 v0.2.1
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
)

// Local fork: fix stdio McpServer MarshalJSON missing "type" field.
// Remove this replace once upstream merges the fix.
replace github.com/coder/acp-go-sdk => ../acp-go-sdk
