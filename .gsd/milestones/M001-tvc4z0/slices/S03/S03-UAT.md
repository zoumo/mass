# S03: RuntimeClass Registry — UAT

**Milestone:** M001-tvc4z0
**Written:** 2026-04-03T02:53:19.752Z

# S03 UAT: RuntimeClass Registry

## Preconditions
1. Go toolchain installed and project builds successfully
2. Working directory is `/Users/jim/code/zoumo/open-agent-runtime`

## Test Cases

### TC01: Registry Creation with Valid Config
**Purpose:** Verify RuntimeClassRegistry loads multiple runtime classes from config

**Steps:**
1. Create test config with 3 runtime classes (python, nodejs, bash)
2. Call `NewRuntimeClassRegistry(configs)`
3. Verify registry contains 3 classes

**Expected Result:**
- Registry created without error
- `registry.List()` returns 3 RuntimeClass objects
- Each class has correct Name, Command, Args, Env fields

---

### TC02: Get Found and Not Found
**Purpose:** Verify Get returns class for valid name, error for invalid name

**Steps:**
1. Create registry with "python" and "nodejs" classes
2. Call `registry.Get("python")`
3. Verify returned class.Name == "python"
4. Call `registry.Get("nonexistent")`
5. Verify error returned with message "runtime class not found: nonexistent"

**Expected Result:**
- Get("python") returns *RuntimeClass with Name="python"
- Get("nonexistent") returns error, nil class

---

### TC03: Environment Variable Substitution
**Purpose:** Verify ${VAR} patterns resolved using os.Getenv

**Steps:**
1. Set environment variable: `export TEST_VAR=resolved_value`
2. Create config with Env containing `"RESOLVED": "${TEST_VAR}"` and `"STATIC": "static_value"`
3. Call `NewRuntimeClassRegistry(configs)`
4. Get class and verify Env["RESOLVED"] == "resolved_value"
5. Verify Env["STATIC"] == "static_value" (unchanged)

**Cleanup:** unset TEST_VAR

**Expected Result:**
- ${TEST_VAR} resolved to "resolved_value"
- Static values unchanged

---

### TC04: Command Required Validation
**Purpose:** Verify empty Command returns validation error

**Steps:**
1. Create config with runtime class having `Command: ""` (empty)
2. Call `NewRuntimeClassRegistry(configs)`
3. Verify error returned with message "runtime class [name]: command is required"
4. Verify registry is nil on error

**Expected Result:**
- Error returned with correct message
- Registry is nil (not created)

---

### TC05: Capabilities Defaults Applied
**Purpose:** Verify default values for unspecified Capabilities

**Steps:**
1. Create config with runtime class having no Capabilities specified
2. Call `NewRuntimeClassRegistry(configs)`
3. Get class and verify:
   - `Capabilities.Streaming == true` (default)
   - `Capabilities.SessionLoad == false` (default)
   - `Capabilities.ConcurrentSessions == 1` (default)

**Expected Result:**
- All defaults applied correctly at registry creation

---

### TC06: List Returns All Classes
**Purpose:** Verify List returns all registered classes as slice

**Steps:**
1. Create registry with 3 classes (python, nodejs, bash)
2. Call `registry.List()`
3. Verify slice length == 3
4. Verify all expected names present in slice

**Expected Result:**
- List() returns []*RuntimeClass with all classes
- No classes missing from list

---

### TC07: Thread-Safe Concurrent Access (Optional)
**Purpose:** Verify Get/List work correctly under concurrent access

**Steps:**
1. Create registry with multiple classes
2. Spawn 10 goroutines calling Get() concurrently
3. Spawn 10 goroutines calling List() concurrently
4. Verify no race conditions, all calls succeed

**Expected Result:**
- No race conditions detected
- All concurrent calls return correct results

---

## Verification Commands

```bash
# Run all RuntimeClass tests
go test ./pkg/agentd/... -v -run RuntimeClass

# Build and static analysis
go build ./pkg/agentd/...
go vet ./pkg/agentd/...
```

**Pass Criteria:** All tests pass, build succeeds, vet clean
