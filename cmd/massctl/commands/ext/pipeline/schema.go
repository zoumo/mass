package pipeline

import (
	"bytes"
	"sync"

	_ "embed"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/pipeline.schema.json
var schemaJSON []byte

var (
	compiledSchema *jsonschema.Schema
	compileOnce    sync.Once
	compileErr     error
)

func getSchema() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
		if err != nil {
			compileErr = err
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource("pipeline.schema.json", doc); err != nil {
			compileErr = err
			return
		}
		compiledSchema, compileErr = c.Compile("pipeline.schema.json")
	})
	return compiledSchema, compileErr
}
