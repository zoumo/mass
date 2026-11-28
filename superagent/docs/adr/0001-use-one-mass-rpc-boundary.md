# Use one MASS RPC boundary in both deployment modes

Superagent Core can start MASS in the same process or use a standalone MASS
instance. Both paths use the public MASS JSON-RPC client and handler. In
embedded mode, Core connects to the handler in process. If operators enable the
HTTP mount, external MASS clients reach the same instance over WebSocket on
Core's listener or through a configured reverse proxy.

Calling the underlying Service directly from embedded Core would give that
mode its own request and subscription path. Using the same client and handler
keeps validation, RPC errors, event delivery, and ownership checks common to
both modes. It also lets operators inspect the Sessions Core uses with ordinary
MASS clients.

The Core and MASS stores retain separate data directories and writer ownership.
The mount requires explicit configuration and a dedicated instance-wide MASS
bearer. Core Source, Operator, and role credentials retain their own authority.
Switch ownership of a MASS data directory only after its previous owner exits.

[Decision commit](https://github.com/zoumo/mass/commit/7bd52cf1d20e14b948248f004add44569bbb2199) ·
[Mount and authentication process tests](../../pkg/core/massd_mount_process_test.go) ·
[MASS WebSocket contract](../../../harness/bindings/websocket.md)
