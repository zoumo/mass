---
estimated_steps: 7
estimated_files: 1
skills_used: []
---

# T04: pkg/events/envelope.go — decodeEventPayload 5 new cases

Update envelope.go decodeEventPayload():
1. Add 5 new cases to both the outer switch and the unmarshal closure type switch:
   - available_commands -> AvailableCommandsEvent
   - current_mode -> CurrentModeEvent
   - config_option -> ConfigOptionEvent
   - session_info -> SessionInfoEvent
   - usage -> UsageEvent

## Inputs

- `pkg/events/envelope.go`

## Expected Output

- `pkg/events/envelope.go with 5 new cases in decodeEventPayload`

## Verification

go build ./pkg/events/...
