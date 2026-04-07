---
draft: true
depends_on: [M003]
---

# M004: Realized Room Runtime and Routing — Context Draft

**Gathered:** 2026-04-07
**Status:** Draft — needs dedicated discussion before planning

## Seed From Prior Discussion

This milestone is the follow-on to contract convergence and safety hardening. It exists to turn the now-conflicting Room ideas into a realized runtime model that can actually be implemented and verified.

The broad intent already established is:

- do not implement Room runtime while Room ownership, delivery semantics, and shared-workspace safety are still moving targets
- after M002 and M003 stabilize the contract and failure model, land the realized Room runtime intentionally rather than as a side effect of ARI surface drift
- keep the distinction between orchestrator desired state and agentd realized state explicit, not implicit

## Likely Focus Areas

- runtime Room object model and lifecycle ownership
- room CRUD semantics in ARI
- member tracking and routeable addressing
- `room_send` / `room_broadcast` delivery semantics
- partial success, busy handling, timeout, correlation, sender/receiver attribution
- shared workspace access mode and isolation rules for multi-agent coordination

## Why This Probably Exists

The current repository already contains signs of Room ambition in multiple places, but the semantics conflict. This milestone exists to turn that design drift into one runtime truth that downstream orchestration can depend on.

## What Needs Future Discussion

- the exact user-facing and orchestrator-facing entrypoints for Room flows
- whether Room runtime should ship with point-to-point routing first, broadcast first, or both together
- what proof level is required before Room can be considered safe with shared workspaces
- how much of Codex/other ACP-client behavior matters for Room-specific validation
