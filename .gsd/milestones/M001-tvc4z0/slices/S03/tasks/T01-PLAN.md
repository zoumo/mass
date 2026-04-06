---
estimated_steps: 16
estimated_files: 3
skills_used: []
---

# T01: Create RuntimeClass registry with types, env substitution, validation, and tests

Fix RuntimeClassConfig struct in config.go and create RuntimeClass registry with types, env substitution, validation, and tests.

## Steps

1. **Fix RuntimeClassConfig in config.go** - Remove Image and Resources fields, add Command string (yaml:"command"), Args []string (yaml:"args,omitempty"), Env map[string]string (yaml:"env,omitempty"), and add CapabilitiesConfig struct with Streaming bool, SessionLoad bool, ConcurrentSessions int all with yaml tags.

2. **Create pkg/agentd/runtimeclass.go** - Define RuntimeClass struct with Name, Command, Args, Env, Capabilities fields. Define Capabilities struct with Streaming (default true), SessionLoad (default false), ConcurrentSessions (default 1). Define RuntimeClassRegistry struct with sync.RWMutex and classes map[string]*RuntimeClass.

3. **Implement NewRuntimeClassRegistry** - Constructor takes map[string]RuntimeClassConfig, validates each entry (Command required), applies Capabilities defaults, performs ${VAR} env substitution using os.Expand on Env values, stores in internal map. Returns error if any RuntimeClass missing Command.

4. **Implement Get and List methods** - Get(name) returns *RuntimeClass or error if not found, uses RLock/RUnlock pattern from ari.Registry. List() returns []*RuntimeClass slice, uses RLock/RUnlock.

5. **Create pkg/agentd/runtimeclass_test.go** - Write tests: TestNewRuntimeClassRegistryValidConfig (load multiple classes), TestGetFoundAndNotFound, TestEnvSubstitution (set env var, verify ${VAR} resolved), TestCommandRequired (missing Command returns error), TestCapabilitiesDefaults (verify defaults applied when not specified).

## Must-Haves

- [ ] RuntimeClassConfig struct has Command/Args/Env/Capabilities fields (Image/Resources removed)
- [ ] RuntimeClass and Capabilities types defined with correct field names and types
- [ ] NewRuntimeClassRegistry validates Command is required, returns error for missing Command
- [ ] ${VAR} env substitution works via os.Expand at registry creation time
- [ ] Capabilities defaults applied: Streaming=true, SessionLoad=false, ConcurrentSessions=1
- [ ] Get(name) returns *RuntimeClass or error, thread-safe with RLock
- [ ] List() returns []*RuntimeClass, thread-safe with RLock
- [ ] Unit tests pass for all scenarios

## Inputs

- `pkg/agentd/config.go`

## Expected Output

- `pkg/agentd/runtimeclass.go`
- `pkg/agentd/runtimeclass_test.go`

## Verification

go test ./pkg/agentd/... -v

## Observability Impact

none
