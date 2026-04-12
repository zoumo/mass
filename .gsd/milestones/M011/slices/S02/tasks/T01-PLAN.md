---
estimated_steps: 5
estimated_files: 2
skills_used: []
---

# T01: Fix broken existing tests

Fix broken existing tests:
1. TestTranslate_AgentMessageChunk, TestTranslate_AgentThoughtChunk, TestTranslate_UserMessageChunk: update assertions to include non-nil Content field
2. TestEventLog_TranslatorWritesCanonicalEnvelope: same
3. Any other existing tests that check TextEvent/ThinkingEvent/UserMessageEvent equality without Content field

Use assert.Equal with full struct or check specific fields instead of full struct equality.

## Inputs

- `pkg/events/translator_test.go`
- `pkg/events/log_test.go`

## Expected Output

- `fixed existing tests`

## Verification

go test ./pkg/events/... -run 'TestTranslate_Agent|TestTranslate_User|TestEventLog_Translator' 2>&1 | grep -E 'PASS|FAIL|ok'
