---
estimated_steps: 8
estimated_files: 1
skills_used: []
---

# T03: pkg/events/translator.go — translate() + convert helpers

Update translator.go:
1. Rewrite translate() to cover all 11 SessionUpdate branches (no nil returns)
2. Add convertContentBlock, convertAnnotations, convertEmbeddedResource
3. Add convertToolCallContents, convertLocations
4. Add convertCommands, convertAvailableCommandInput
5. Add convertConfigOptions, convertConfigSelectOptions, convertConfigCategory
6. Add convertCost
7. Add safeToolKind, safeStringPtr helpers

## Inputs

- `pkg/events/translator.go`
- `ACP SDK types_gen.go`

## Expected Output

- `pkg/events/translator.go with translate() covering all 11 branches`

## Verification

go build ./pkg/events/...
