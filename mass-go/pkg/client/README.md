# Generated HTTP client

`client.Client` is the MASS facade used by massctl. Resource methods use the
[OpenAPI-generated types and request builders](generated/client.gen.go),
then execute requests directly through a shared Resty client. Pass raw Session
IDs in SessionRef. Paths are escaped once by the generated request builder; complete `.` and `..`
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

`client.New(url, token)` initializes one [Resty v3.0.0-rc.4](https://github.com/go-resty/resty/releases/tag/v3.0.0-rc.4)
client, shared by ordinary requests and SSE. It configures Bearer authentication,
disables retries and redirects, and preserves explicit request body lengths.
`Client.HTTP` is the `*resty.Client` from `resty.dev/v3`; set its transport and
timeout before use. Call `Client.Close()` after all requests and observers end;
the CLI closes its client on every exit path.
Do not enable retries, redirects or automatic response parsing. Request contexts
control deadlines by default. Resty v3 timeouts also use context; a client-wide
timeout limits an SSE stream's entire lifetime. The facade closes ordinary
response bodies. A failed wait does not cancel Agent work.
Requests carry the generated Content-Length through the request-level
`SetContentLength(int64)` API. Resty response parsing is disabled with
`SetResponseDoNotParse(true)` so the facade retains JSON number precision
and reads SSE incrementally. Both paths read and close Resty's wrapped
`Response.Body`, which retains timeout cleanup and content decompression.
Observe returns only after subscription.ready; Next distinguishes closed, error
and unexpected EOF. Observer.Close releases just this observation. There is no
automatic reconnect or resume, and no arbitrary frame-size limit.

Client/HTTP/CLI and fixed SDK mock evidence verifies protocol and deterministic
workflows. It does not establish real Agent/model or deployed-proxy acceptance.
