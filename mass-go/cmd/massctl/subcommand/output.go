package subcommand

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

func (o *options) print(w io.Writer, raw json.RawMessage) error {
	if o.output == "json" {
		return json.NewEncoder(w).Encode(raw)
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return err
	}
	switch v := value.(type) {
	case []any:
		return resourceTable(w, v)
	case map[string]any:
		if items, ok := v["items"].([]any); ok {
			if err := resourceTable(w, items); err != nil {
				return err
			}
			if next, ok := v["next"]; ok {
				_, err := fmt.Fprintln(w, "Next:", next)
				return err
			}
			return nil
		}
		if _, ok := v["metadata"]; ok {
			if err := resourceTable(w, []any{v}); err != nil {
				return err
			}
			metadata, _ := v["metadata"].(map[string]any)
			status, _ := v["status"].(map[string]any)
			_, err := fmt.Fprintf(w, "Generation: %v  Applied generation: %v\n", field(metadata, "generation"), field(status, "appliedGeneration"))
			return err
		}
	}
	// Receipts and history retain every business field, including false/absent evidence.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, pretty.String())
	return err
}

func field(v map[string]any, key string) any {
	if x, ok := v[key]; ok {
		return x
	}
	return "-"
}

func resourceTable(w io.Writer, items []any) error {
	t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(t, "NAME\tNAMESPACE\tID\tRUNTIME/TYPE\tSTATE"); err != nil {
		return err
	}
	for _, item := range items {
		v, _ := item.(map[string]any)
		m, _ := v["metadata"].(map[string]any)
		s, _ := v["status"].(map[string]any)
		spec, _ := v["spec"].(map[string]any)
		ref := field(spec, "runtimeRef")
		if ref == "-" {
			ref = field(spec, "type")
		}
		if _, err := fmt.Fprintf(t, "%v\t%v\t%v\t%v\t%v\n", field(m, "name"), field(m, "namespace"), field(s, "sessionId"), ref, field(s, "state")); err != nil {
			return err
		}
	}
	return t.Flush()
}
