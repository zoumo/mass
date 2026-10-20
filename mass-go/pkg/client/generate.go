//go:build ignore

// Generate from the registered HTTP contract without starting a server or Agent.
package main

import (
	"encoding/json"
	"os"
	"os/exec"

	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func main() {
	b, err := transport.OpenAPI()
	must(err)
	must(os.WriteFile("generated/openapi.json", append(b, '\n'), 0o644))
	var doc map[string]any
	must(json.Unmarshal(b, &doc))
	// Apply Go-only hints to schemas, never change wire types or constraints.
	// Raw unions avoid a second event model and preserve future open values.
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, schema := range schemas {
		preserveSchema(schema)
	}
	for _, path := range doc["paths"].(map[string]any) {
		for _, operation := range path.(map[string]any) {
			op, ok := operation.(map[string]any)
			if !ok {
				continue
			}
			if params, ok := op["parameters"].([]any); ok {
				for _, parameter := range params {
					p := parameter.(map[string]any)
					// The wire parameter runtime otherwise shadows the generated runtime import.
					if p["in"] == "path" && p["name"] == "runtime" {
						p["x-go-name"] = "runtimeName"
					}
				}
			}
			if body, ok := op["requestBody"].(map[string]any); ok {
				if content, ok := body["content"].(map[string]any); ok {
					for _, media := range content {
						preserveSchema(media.(map[string]any)["schema"])
					}
				}
			}
		}
	}
	b, err = json.MarshalIndent(doc, "", "  ")
	must(err)
	f, err := os.CreateTemp("", "mass-client-*.json")
	must(err)
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	must(err)
	must(f.Close())
	cmd := exec.Command("go", "run", "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0", "-config", "oapi-codegen.yaml", f.Name())
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	must(cmd.Run())
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// Visit only JSON Schema children; default/example objects are data, not schemas.
func preserveSchema(value any) {
	s, ok := value.(map[string]any)
	if !ok {
		return
	}
	_, one := s["oneOf"]
	_, anyOf := s["anyOf"]
	if len(s) == 0 || one || anyOf {
		s["x-go-type"] = "rawjson.RawMessage"
		s["x-go-type-import"] = map[string]any{"path": "encoding/json", "name": "rawjson"}
		s["x-go-type-skip-optional-pointer"] = true
	}
	for _, key := range []string{"items", "additionalProperties", "not"} {
		preserveSchema(s[key])
	}
	if properties, ok := s["properties"].(map[string]any); ok {
		for _, v := range properties {
			preserveSchema(v)
		}
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if branches, ok := s[key].([]any); ok {
			for _, v := range branches {
				preserveSchema(v)
			}
		}
	}
}
