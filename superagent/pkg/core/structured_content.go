package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

const structuredContentObjectCheck = `CASE WHEN typeof(content)='text' AND json_valid(content)
 THEN json_type(content)='object' ELSE 0 END`

type StructuredContent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type markdownContentData struct {
	Text string `json:"text"`
}

type contentOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type optionsContentData struct {
	Prompt  string          `json:"prompt"`
	Options []contentOption `json:"options"`
}

type contentSchemaSummary struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type contentSchema struct {
	contentSchemaSummary
	Schema   map[string]any    `json:"schema"`
	Example  StructuredContent `json:"example"`
	validate func(json.RawMessage) (any, error)
}

var contentSchemas = []contentSchema{
	{
		contentSchemaSummary: contentSchemaSummary{"markdown", "Send plain text or formatted Markdown."},
		Schema:               objectSchema(map[string]any{"text": contentStringSchema()}, "text"),
		Example:              StructuredContent{Type: "markdown", Data: json.RawMessage(`{"text":"**Checks passed.**"}`)},
		validate:             validateMarkdownContent,
	},
	{
		contentSchemaSummary: contentSchemaSummary{"options", "Present a Markdown prompt and a list of choices."},
		Schema: objectSchema(map[string]any{
			"prompt": contentStringSchema(),
			"options": map[string]any{
				"type": "array", "minItems": 2, "description": "Option IDs must be unique.",
				"items": objectSchema(map[string]any{"id": contentStringSchema(), "label": contentStringSchema()}, "id", "label"),
			},
		}, "prompt", "options"),
		Example:  StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"**Checks passed.** What next?","options":[{"id":"continue","label":"Continue"},{"id":"pause","label":"Pause"}]}`)},
		validate: validateOptionsContent,
	},
}

func contentStringSchema() map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "description": "Must contain a non-whitespace character and no NUL characters."}
}

func structuredContentInputSchema() map[string]any {
	types := make([]string, 0, len(contentSchemas))
	for _, schema := range contentSchemas {
		types = append(types, schema.Type)
	}
	return objectSchema(map[string]any{
		"type": map[string]any{"type": "string", "enum": types},
		"data": map[string]any{"type": "object", "description": "Use message_content_schema_get for the fields of this content type."},
	}, "type", "data")
}

func contentSchemaByType(name string) (contentSchema, bool) {
	for _, schema := range contentSchemas {
		if schema.Type == name {
			return schema, true
		}
	}
	return contentSchema{}, false
}

func (c *StructuredContent) normalize() error {
	if c == nil || c.Type == "" {
		return problem("invalid_argument", "content.type is required")
	}
	schema, ok := contentSchemaByType(c.Type)
	if !ok {
		return problem("invalid_argument", "content.type is not supported")
	}
	if len(c.Data) == 0 || c.Data[0] != '{' {
		return problem("invalid_argument", "content.data must be an object")
	}
	validated, err := schema.validate(c.Data)
	if err != nil {
		return err
	}
	c.Data, err = json.Marshal(validated)
	return err
}

func validateContentString(value, path string) error {
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
		return problem("invalid_argument", path+" must be nonblank and contain no NUL characters")
	}
	return nil
}

func validateMarkdownContent(raw json.RawMessage) (any, error) {
	var data markdownContentData
	if err := decodeStrict(raw, &data); err != nil {
		return nil, problem("invalid_argument", "content.data for markdown must contain a text string")
	}
	return data, validateContentString(data.Text, "content.data.text")
}

func validateOptionsContent(raw json.RawMessage) (any, error) {
	var data optionsContentData
	if err := decodeStrict(raw, &data); err != nil {
		return nil, problem("invalid_argument", "content.data for options must contain a prompt string and an options array of id and label strings")
	}
	if err := validateContentString(data.Prompt, "content.data.prompt"); err != nil {
		return nil, err
	}
	if len(data.Options) < 2 {
		return nil, problem("invalid_argument", "content.data.options must contain at least two options")
	}
	ids := make(map[string]struct{}, len(data.Options))
	for i, option := range data.Options {
		path := fmt.Sprintf("content.data.options[%d]", i)
		if err := validateContentString(option.ID, path+".id"); err != nil {
			return nil, err
		}
		if err := validateContentString(option.Label, path+".label"); err != nil {
			return nil, err
		}
		if _, exists := ids[option.ID]; exists {
			return nil, problem("invalid_argument", path+".id must be unique")
		}
		ids[option.ID] = struct{}{}
	}
	return data, nil
}
