# M011: Reduce Shim Event Translation Overhead

## Vision
Replace over-aggressive ACP event translation with faithful mirroring — all SessionUpdate branches preserved, union types use flat ACP wire shape, upstream consumers get complete data to work with.

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | S01 | low | — | ✅ | go build ./... passes; translate() covers all SessionUpdate branches with no nil returns |
| S02 | S02 | low | — | ✅ | go test ./pkg/events/... passes with full coverage of all new types and JSON shape alignment |
