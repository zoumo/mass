# M014: Enrich state.json + Session Metadata Pipeline

## Vision
state.json becomes a reliable session capability snapshot — agentInfo, capabilities, commands, config options, mode, title written from ACP notifications; every metadata change emits a state_change event with sessionChanged field; EventCounts tracks all event types; writeState is safe read-modify-write so Kill/exit never clobbers session data; dead placeholder event types removed.

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | S01 | low | — | ⬜ | After this: `! rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'` returns no output; `go test ./pkg/shim/...` passes. |
| S02 | state.json type definitions | high | — | ⬜ | After this: round-trip test WriteState({Session: full SessionState with ConfigOption Select variant + AvailableCommandInput Unstructured}) → ReadState reproduces identical values; EventCounts and UpdatedAt survive round-trip. |
| S03 | writeState read-modify-write refactor | medium | S02 | ⬜ | After this: test proves Kill() → state.json.status==stopped AND state.json.session (previously written by bootstrap-complete closure) still present; process-exit similarly; UpdatedAt present on every write; EventCounts flushed on every write. |
| S04 | Translator eventCounts | medium | S01 | ⬜ | After this: test runs a prompt turn through mockagent; Translator.EventCounts() returns {text: N, tool_call: M, turn_start: 1, turn_end: 1, user_message: 1, state_change: K}; injecting a failing log proves counts stay at 0 on failed append. |
| S05 | ACP bootstrap capabilities capture | medium | S02, S03 | ⬜ | After this: test runs Manager.Create() with a mock ACP server that returns a populated InitializeResponse; ReadState() shows state.json.session.agentInfo.name matches the mock response; state.json.session.capabilities.loadSession matches; bootstrap-metadata state_change event appears in event log. |
| S06 | Session metadata hook chain | high | S03, S04 | ⬜ | After this: inject a ConfigOptionUpdate ACP notification into a running translator; state.json.session.configOptions updated; event log contains exactly one state_change with reason:config-updated and sessionChanged:[configOptions]; Kill() afterwards — state.json still has configOptions. |
| S07 | runtime/status overlay + doc updates | low | S04, S06 | ⬜ | After this: test calls Status() with a Translator that has in-memory counts different from state.json; response.State.EventCounts matches Translator memory not file; all acceptance criteria from plan doc pass; make build + go test ./... passes. |
