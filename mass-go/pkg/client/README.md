# Generated HTTP client

`client.Client` is the MASS facade used by massctl. Resource methods call the
[OpenAPI-generated client](generated/client.gen.go); there is no operation-string
SDK or parallel manual HTTP dispatcher. Pass raw Session IDs in SessionRef.
Paths are escaped once by the generated request builder; complete `.` and `..`
segments and malformed references are rejected before constructing requests.

Regenerate from the current registered server operations, without starting a
listener, loading a Store or starting an Agent:

```sh
cd mass-go
rtk proxy go generate ./pkg/client
```

The build-ignored `generate.go` exports the exact Huma OpenAPI 3.1 contract to
`generated/openapi.json`, then applies Go-only hints to a temporary generation
input. [oapi-codegen v2.8.0](https://github.com/oapi-codegen/oapi-codegen/releases/tag/v2.8.0)
is pinned in that command; its configuration is `oapi-codegen.yaml`. Generated
files must not be edited. No schema downgrade or alternate SDK is maintained.

Open JSON and unions use json.RawMessage, including optional explicit null.
The facade builds generated writable request models and decodes responses with
UseNumber, preserving open JSON integers such as 9007199254740993. Events decode
into the existing events.SessionEvent, not a second event representation.
`Answer(ctx, ref, interaction, nil, out)` sends literal JSON null. CLI answer
parameters remain deferred; there is no answer command in this release.

The facade injects Bearer authentication, rejects redirect following, closes
ordinary response bodies and does not replay mutations. A custom HTTP transport
must also respect that no-replay policy. A failed wait does not cancel Agent work.
SSE uses the generated raw response method, never WithResponse (which reads the
entire body). Observe returns only after subscription.ready; Next distinguishes
closed, error and unexpected EOF. Close releases just this observation. There is
no automatic reconnect or resume, and no arbitrary frame-size limit.

Client/HTTP/CLI and fixed SDK mock evidence verifies protocol and deterministic
workflows. It does not establish real Agent/model or deployed-proxy acceptance.
