---
estimated_steps: 6
estimated_files: 1
skills_used: []
---

# T01: api/events.go — new event type constants

Add 5 new event type constants to api/events.go:
- EventTypeAvailableCommands = "available_commands"
- EventTypeCurrentMode = "current_mode"
- EventTypeConfigOption = "config_option"
- EventTypeSessionInfo = "session_info"
- EventTypeUsage = "usage"

## Inputs

- `docs/plan/reduce-event-translation-20260412.md`
- `api/events.go`

## Expected Output

- `api/events.go with 5 new constants`

## Verification

grep -c EventTypeAvailableCommands api/events.go && grep -c EventTypeUsage api/events.go
