// massctl reads committed local identity/history without constructing a Driver.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

func run() error {
	data := flag.String("data", "", "absolute data directory")
	id := flag.String("id", "", "native session ID (exclusive with namespace/name)")
	namespace := flag.String("namespace", "", "session namespace")
	name := flag.String("name", "", "session name")
	history := flag.Bool("history", false, "read committed history")
	after := flag.String("after", "", "opaque history cursor")
	limit := flag.Int("limit", 100, "history page size")
	flag.Parse()
	st, err := file.Open(*data, true)
	if err != nil {
		return err
	}
	defer st.Close()
	svc := service.New(st, st, nil)
	ref := resource.SessionRef{SessionID: *id, Namespace: *namespace, Name: *name}
	var value any
	if *history {
		value, err = svc.ListHistoryEvents(ref, store.HistoryQuery{After: *after, Limit: *limit})
	} else {
		value, err = svc.GetSession(ref)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(value)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
