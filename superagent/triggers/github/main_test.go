package github

import (
	"os"
	"testing"

	"github.com/zoumo/mass/superagent/triggers/internal/triggertest"
)

var triggerBinary string

func TestMain(m *testing.M) {
	triggerBinary = triggertest.TriggerBinary()
	os.Exit(triggertest.Run(m,
		triggertest.Binary{Path: &triggerBinary, Name: "superagent", Package: "../../../cmd/superagent"},
	))
}
