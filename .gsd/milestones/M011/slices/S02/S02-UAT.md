# S02: Fix and extend tests — UAT

**Milestone:** M011
**Written:** 2026-04-12T14:42:27.701Z

# S02 UAT

## Tests
- `go test ./pkg/events/...` — 62 tests pass (0 failures)
- Breakdown: 10 log, 21 translator, 16 rich translate, 15 wire shape

## Coverage
- All 22 test matrix items from plan covered
- Test 16-20: JSON shape alignment (ContentBlock 5 variants, ToolCallContent 3, AvailableCommandInput, ConfigOption ungrouped+grouped, EmbeddedResource text+blob)
- Test 21-22: Union error cases (empty variant marshal + unknown type unmarshal)

## Build
- `make build` passes
