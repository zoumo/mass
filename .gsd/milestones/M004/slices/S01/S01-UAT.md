# S01: Room Lifecycle and ARI Surface — UAT

**Milestone:** M004
**Written:** 2026-04-08T04:47:16.693Z

## UAT: Room Lifecycle and ARI Surface (S01)

### Preconditions
- agentd built and running with a valid config.yaml (socket, workspaceRoot, metaDB)
- ARI socket reachable for JSON-RPC calls
- At least one runtime class configured (e.g. mockagent) and a prepared workspace

---

### Test 1: Room Create and Status Round-Trip

**Steps:**
1. Call `room/create` with `{ "name": "uat-room-1", "communication": { "mode": "mesh" } }`
2. Verify response contains `name: "uat-room-1"`, `communicationMode: "mesh"`, and a non-empty `createdAt`
3. Call `room/status` with `{ "name": "uat-room-1" }`
4. Verify response contains `name: "uat-room-1"`, `communicationMode: "mesh"`, `members: []` (empty — no sessions yet)

**Expected:** Room is created and queryable. Members list is empty before any sessions join.

---

### Test 2: Members Appear in room/status After session/new

**Steps:**
1. Create a room: `room/create { "name": "uat-room-2" }`
2. Prepare a workspace (emptyDir or existing)
3. Call `session/new` with `room: "uat-room-2"`, `roomAgent: "agent-alpha"`, a valid runtimeClass and workspaceId
4. Call `session/new` with `room: "uat-room-2"`, `roomAgent: "agent-beta"`, same workspace
5. Call `room/status { "name": "uat-room-2" }`
6. Verify `members` array has exactly 2 entries: one with `agentName: "agent-alpha"` and one with `agentName: "agent-beta"`, each with a valid sessionId and state

**Expected:** Both members are visible in room/status with correct agent names and session IDs.

---

### Test 3: Room Deletion With Active Members Refused

**Steps:**
1. Using the room and sessions from Test 2 (or recreate)
2. Call `room/delete { "name": "uat-room-2" }` while sessions are still active
3. Verify the response is an error indicating active members exist

**Expected:** room/delete returns an error — cannot delete a room with non-stopped sessions.

---

### Test 4: Room Deletion After Session Teardown

**Steps:**
1. Stop or remove all sessions associated with the room from Test 2
2. Call `room/delete { "name": "uat-room-2" }`
3. Verify success response
4. Call `room/status { "name": "uat-room-2" }`
5. Verify error: room not found

**Expected:** Room is deleted after all sessions are removed. Subsequent status query returns not-found.

---

### Test 5: Duplicate Room Name Rejected

**Steps:**
1. Call `room/create { "name": "dup-test" }`
2. Call `room/create { "name": "dup-test" }` again
3. Verify second call returns an error indicating duplicate name

**Expected:** Room names are unique — duplicate creation fails.

---

### Test 6: session/new Validates Room Existence (D051)

**Steps:**
1. Call `session/new` with `room: "nonexistent-room"`, `roomAgent: "agent-x"`, valid workspace and runtimeClass
2. Verify error response mentioning that the room does not exist and suggesting `room/create`
3. Call `session/new` with `room: "some-room"` but with empty `roomAgent`
4. Verify error response requiring `roomAgent` when `room` is specified

**Expected:** session/new rejects sessions referencing non-existent rooms and requires roomAgent when room is set.

---

### Test 7: Communication Mode Variants

**Steps:**
1. Call `room/create { "name": "mode-mesh", "communication": { "mode": "mesh" } }` — verify status returns `communicationMode: "mesh"`
2. Call `room/create { "name": "mode-star", "communication": { "mode": "star" } }` — verify status returns `communicationMode: "star"`
3. Call `room/create { "name": "mode-isolated", "communication": { "mode": "isolated" } }` — verify status returns `communicationMode: "isolated"`
4. Call `room/create { "name": "mode-default" }` (no communication field) — verify status returns `communicationMode: "mesh"` (default)

**Expected:** All three modes (mesh/star/isolated) persist correctly. Omitting mode defaults to mesh.

---

### Edge Cases

- **Empty room name:** `room/create { "name": "" }` should return a validation error.
- **room/status for non-existent room:** Should return a not-found error, not an empty result.
- **session/new without room field:** Should work normally (backward compatible — room is optional).
