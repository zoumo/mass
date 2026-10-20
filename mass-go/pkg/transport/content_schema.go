package transport

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"reflect"
)

// ContentBlock has private union fields and a custom JSON codec. Describe that
// wire union in the transport registry, without coupling domain values to Huma.
func registerWorkSchemas(r huma.Registry) {
	object := func(properties map[string]*huma.Schema, required ...string) *huma.Schema {
		return &huma.Schema{Type: "object", Properties: properties, Required: required, AdditionalProperties: false}
	}
	text := func() *huma.Schema { return &huma.Schema{Type: "string"} }
	binary := func() *huma.Schema {
		return &huma.Schema{Type: "string", ContentEncoding: "base64", Pattern: `^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$`}
	}
	zero, one := float64(0), float64(1)
	annotations := object(map[string]*huma.Schema{
		"audience":     {Type: "array", Items: &huma.Schema{Type: "string", Enum: []any{"user", "assistant"}}},
		"priority":     {Type: "number", Minimum: &zero, Maximum: &one},
		"lastModified": {Type: "string", Format: "date-time"},
	})
	resource := &huma.Schema{OneOf: []*huma.Schema{
		object(map[string]*huma.Schema{"uri": text(), "mimeType": text(), "text": text()}, "uri", "text"),
		object(map[string]*huma.Schema{"uri": text(), "mimeType": text(), "blob": binary()}, "uri", "blob"),
	}}
	branches := []*huma.Schema{}
	for _, kind := range []string{"text", "image", "audio", "resource", "resource_link"} {
		props := map[string]*huma.Schema{"type": {Type: "string", Const: kind}, "annotations": annotations}
		required := []string{"type"}
		switch kind {
		case "text":
			props["text"] = text()
			required = append(required, "text")
		case "image", "audio":
			props["data"], props["mimeType"] = binary(), text()
			required = append(required, "data", "mimeType")
			if kind == "image" {
				props["uri"] = text()
			}
		case "resource":
			props["resource"] = resource
			required = append(required, "resource")
		case "resource_link":
			for _, key := range []string{"uri", "name", "mimeType", "title", "description"} {
				props[key] = text()
			}
			props["size"] = &huma.Schema{Type: "integer", Minimum: &zero}
			required = append(required, "uri", "name")
		}
		branches = append(branches, object(props, required...))
	}
	schema := r.Schema(reflect.TypeFor[events.ContentBlock](), false, "")
	*schema = huma.Schema{OneOf: branches, Discriminator: &huma.Discriminator{PropertyName: "type"}}
	schema.PrecomputeMessages()
	input := r.Schema(reflect.TypeFor[runtime.Input](), false, "")
	minimum := 1
	input.Properties["content"].MinItems = &minimum
	input.Properties["content"].Nullable = false
	input.PrecomputeMessages()
}
