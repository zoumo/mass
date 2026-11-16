package sa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func containsSequence(args, sequence []string) bool {
	if len(sequence) == 0 || len(args) < len(sequence) {
		return false
	}
	for start := 0; start <= len(args)-len(sequence); start++ {
		matched := true
		for offset, value := range sequence {
			if args[start+offset] != value {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

var massSABinary string

func TestMain(tests *testing.M) {
	dir, err := os.MkdirTemp("", "mass-sa-tests-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	massSABinary = filepath.Join(dir, "mass-sa")
	build := exec.Command("go", "build", "-o", massSABinary, "./cmd/mass-sa")
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "mass-sa test binary: build failed: %v\n%s\n", err, output)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := tests.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
