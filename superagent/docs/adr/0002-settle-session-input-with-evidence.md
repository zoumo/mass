# Settle Session input with acceptance evidence

Core commits an Internal Event with the business change that created it. A
separate dispatch submits the event to its current SessionBinding. If Core
loses the MASS response or exits before saving it, native effects may already
have occurred. The Core transaction cannot include those effects.

Core marks delivery `sent` only after acceptance evidence. Unsettled events
retain their IDs and can replay to the same verified Session when normal
admission permits it. The receiving role can deduplicate by `event_id`.
Resuming or replaying still carries the risk of repeated native effects.

When the original binding needs reconciliation, an operator records checked
facts and acknowledges that risk before requesting retry. A replacement
requires evidence that the original authority was released. A missing
response, idle observation, or Session status alone cannot prove that a
particular native operation ran or that its owner was released. Sent events
retain their acceptance history. Retired Issue Session events are superseded
if they were still unconfirmed.

[SessionBinding decision commit](https://github.com/zoumo/mass/commit/0e42c2d89241608388da016fd612a8202946be2e) ·
[Confirmed authority loss fix](https://github.com/zoumo/mass/commit/93fcaa1809c47059504ebbf36c49bbc8bcbec93d) ·
[Lost receipt and retirement process tests](../../pkg/core/internal_event_process_test.go) ·
[Operator recovery](../../API.md#local-operator-reconciliation)
