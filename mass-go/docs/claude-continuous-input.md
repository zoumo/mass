# Claude continuous input: attribution boundary

This implementation uses the fixed SDK
`v0.0.0-20261106173112-83036ea07a0c`. No native TurnID is created.

## What the receipt proves

Each successfully written input returns a distinct MASS `submission` UUID and
`acceptance.sent=true`. Native acceptance and native recording remain absent.
The UUID identifies MASS work, not a native turn, execution acknowledgment, or
exactly-once guarantee. One unresolved input is allowed per connection.

The first input uses the native text form. After its root result settles, later
inputs use structured native user input with a caller `uuid`. This is an input
field supported by the SDK, not a guarantee that Claude echoes it on output.
All inputs use the original SDK Session and its one Receive consumer.

## Native facts and current state

Every converted content event carries `_meta.claude.attribution`, and only an
attributed fact carries `_meta.claude.submission`. These fields survive SSE and
public history readback; execution-only Source retains the original run.

- `single_submission`: local single-input policy for the first input, when no
  previous local input can own a delayed fact. This is not native input acceptance
  or universally proven causality. Pre-input facts have unknown work attribution.
- `user_message_uuid`: a native envelope explicitly echoes a known caller UUID.
  The adapter does not infer this from event adjacency, text, request IDs, message
  IDs, result UUIDs, child run IDs, or mutable current-work state.
- `prior_result`: the result UUID identifies a previously observed settled result.
  This can recognize an old result without assigning it to newer work.
- `unknown`: no supported evidence links the fact to a MASS submission. Ordinary
  assistant content without linkage after the first input is preserved in arrival
  order with unknown attribution, not relabeled as current input output.

A known old result or confirmed child result cannot settle the active input.
A result with conflicting known result identity and caller linkage is unknown,
not a reason to prefer the current submission.
A later root result without proven linkage emits `work_attribution_unknown` and
sets current observed state to unknown. Work remains unresolved and another input
is rejected. An unlinked interaction after the first input is not admitted as
current answer authority; it reports `interaction_attribution_unknown`. A known
old interaction cannot change the new work's state. No automatic retry, inferred
TurnID, fabricated UUID echo, or process replacement resolves this uncertainty.

An interrupt acknowledgment alone is not settlement. Only a root result linked
to the current work (or the first input's single-input boundary) reopens admission.
An explicitly `NotSent` submission failure releases its local reservation;
unknown dispatch retains the unresolved responsibility. Attribution storage is
bounded to 128 admitted submissions and 128 distinct settled result UUIDs per
connection; exhausted evidence capacity rejects/fails explicitly, without eviction
that could reassign old facts.

## Support and validation limits

Same-connection repeated writes are supported. **General real-model continuous
input with complete event-to-input attribution is not established.** In
particular, normal native assistant/result frames may lack
`user_message_uuid`/`request_id`. Request IDs alone do not prove Submit ownership,
and this adapter does not treat them as caller UUIDs. A later unlinked result
therefore prevents further input until actual current-work evidence settles it
or the original connection is explicitly released through lifecycle coordination.

The dedicated synthetic peer deliberately echoes caller UUIDs for positive
cases, and omits them for the unknown case. Its append-only input evidence proves
wire inputs reached the same PID. These tests prove MASS policy under controlled
native frames, not that real Claude or a particular model supplies such linkage.
No real-model acceptance was run for this ticket. Follow-up real acceptance for
both providers must use the requested theta/glm-5.3 configuration and independently
inspect ordinary native linkage; synthetic success must not be reported as that
acceptance. Lifecycle ticket 04 and history load are outside this change.
