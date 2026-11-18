package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "mass-sa is retired; use superagent serve with a new data directory. Existing JSON v4 data is not migrated or removed.")
	os.Exit(1)
}
