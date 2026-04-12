---
estimated_steps: 9
estimated_files: 1
skills_used: []
---

# T03: Tests 16-22: JSON shape alignment + union error cases

New test file pkg/events/wire_shape_test.go covering test matrix items 16-22:

16. ContentBlock JSON shape: 5 variants - construct ACP SDK object, marshal, compare JSON key layout to OAR mirror marshal
17. ToolCallContent JSON shape: content/diff/terminal 3 variants ACP vs OAR marshal alignment
18. AvailableCommandInput JSON shape: unstructured variant flat shape alignment
19. ConfigOption JSON shape: select variant + options (ungrouped/grouped) alignment
20. EmbeddedResource JSON shape: text/blob variants (no type field, field-presence discriminated)
21. Union empty variant marshal error: ContentBlock zero variant -> error, ToolCallContent zero -> error
22. Union unknown type unmarshal error: ContentBlock unknown type -> error with type name, ToolCallContent unknown -> error with type name

For shape tests: json.Marshal ACP SDK object -> map[string]any, json.Marshal OAR object -> map[string]any, compare keys present (except sessionUpdate).

## Inputs

- `pkg/events/types.go`

## Expected Output

- `pkg/events/wire_shape_test.go with ACP shape alignment tests`

## Verification

go test ./pkg/events/... -run TestWireShape -v 2>&1 | tail -20
