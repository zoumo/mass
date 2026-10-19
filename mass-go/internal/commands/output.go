package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

func (o *options) print(out io.Writer, method string, raw json.RawMessage, page bool) error {
	encoder := json.NewEncoder(out)
	if method == "agentd/session/get_event" && !page {
		var history struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(raw, &history); err != nil {
			return err
		}
		for _, event := range history.Events {
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		return nil
	}
	if !o.json && o.output != "json" {
		switch method {
		case "agentd/runtime/list":
			return table(out, raw, []string{"NAME", "TYPE", "ENABLED", "PHASE", "REASON"}, []string{"name", "type", "enabled", "status.phase", "status.unavailableReason"})
		case "agentd/runtime/types":
			return table(out, raw, []string{"TYPE"}, []string{"type"})
		case "agentd/session/list":
			return table(out, raw, []string{"NAME", "SESSION-ID", "RUNTIME", "LIFECYCLE", "CONNECTED", "CWD"}, []string{"sessionName", "sessionId", "spec.runtimeName", "lifecycle", "connected", "spec.cwd"})
		}
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(raw)
}

func table(out io.Writer, raw json.RawMessage, headers, paths []string) error {
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		cells := make([]string, len(paths))
		for i, path := range paths {
			var value any = row
			for _, key := range strings.Split(path, ".") {
				m, _ := value.(map[string]any)
				value = m[key]
			}
			cells[i] = "-"
			if value != nil {
				if text, ok := value.(string); ok {
					cells[i] = strings.Join(strings.Fields(text), " ")
				} else {
					b, _ := json.Marshal(value)
					cells[i] = string(b)
				}
			}
		}
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return w.Flush()
}
