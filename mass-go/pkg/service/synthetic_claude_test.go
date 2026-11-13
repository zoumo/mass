package service_test

import (
	"fmt"
	"os"

	nativeclaude "github.com/zoumo/agent-sdk-go/claude"
)

type syntheticClaudeControlFrame struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype string `json:"subtype"`
		CWD     string `json:"cwd"`
	} `json:"request"`
}

func answerSyntheticClaudeVersion() {
	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			fmt.Fprintln(os.Stdout, nativeclaude.MinimumCLIVersion+" (Claude Code)")
			os.Exit(0)
		}
	}
}

func syntheticClaudeVersionShell() string {
	return "if [ \"$1\" = \"--version\" ]; then echo '" + nativeclaude.MinimumCLIVersion + " (Claude Code)'; exit 0; fi\n"
}
