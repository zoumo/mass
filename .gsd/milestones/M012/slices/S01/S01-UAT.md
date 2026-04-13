# S01: pkg/jsonrpc/ Transport-Agnostic Framework — UAT

**Milestone:** M012
**Written:** 2026-04-13T16:11:19.940Z

## Phase 1 UAT\n\n- [x] 18 protocol tests pass\n- [x] Server.Serve accepts net.Listener (transport-agnostic)\n- [x] Client wraps jsonrpc2 with bounded FIFO worker\n- [x] RPCError preserves code/message\n- [x] Peer.Notify works from handler context\n- [x] make build passes
