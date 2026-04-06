# S03 Research: RuntimeClass Registry

## Summary

Implement RuntimeClass registry that resolves runtimeClass names to launch configurations (Command/Args/Env/Capabilities) with environment variable substitution. This is a configuration-level registry loaded from config.yaml at daemon startup.

**Requirement**: R002 — RuntimeClass registry can resolve runtimeClass name to command/args/env/capabilities

## Recommendation

Create `pkg/agentd/runtimeclass.go` with:
1. **RuntimeClass struct** - aligned with design spec (Command, Args, Env, Capabilities)
2. **RuntimeClassRegistry** - thread-safe map with sync.RWMutex, Get/List methods
3. **Env substitution** - `${VAR}` → os.Getenv at load time
4. **Validation** - Command required, Capabilities defaults applied

Modify `pkg/agentd/config.go` to fix RuntimeClassConfig struct.

## Implementation Landscape

### Files to Modify

| File | What Changes |
|------|--------------|
| `pkg/agentd/config.go` | Fix RuntimeClassConfig: remove Image/Resources, add Command/Args/Capabilities |

### Files to Create

| File | Purpose |
|------|---------|
| `pkg/agentd/runtimeclass.go` | RuntimeClass type, Capabilities type, Registry struct with Get/List |
| `pkg/agentd/runtimeclass_test.go` | Registry tests: load, resolve, env substitution, validation |

### Key Types

```go
// RuntimeClass defines how to launch a specific agent type.
type RuntimeClass struct {
    Name         string            // from config key
    Command      string            // REQUIRED - executable path
    Args         []string          // optional - command line args
    Env          map[string]string // optional - resolved env vars
    Capabilities Capabilities      // optional - agent capabilities
}

// Capabilities declares what the agent supports.
type Capabilities struct {
    Streaming          bool // default: true
    SessionLoad        bool // default: false  
    ConcurrentSessions int  // default: 1
}

// RuntimeClassRegistry resolves runtimeClass names to configurations.
type RuntimeClassRegistry struct {
    mu     sync.RWMutex
    classes map[string]*RuntimeClass
}
```

### Registry API

```go
// NewRuntimeClassRegistry creates registry from config map.
// Performs ${VAR} env substitution and validation.
func NewRuntimeClassRegistry(classes map[string]RuntimeClassConfig) (*RuntimeClassRegistry, error)

// Get returns RuntimeClass by name, or error if not found.
func (r *RuntimeClassRegistry) Get(name string) (*RuntimeClass, error)

// List returns all registered RuntimeClasses.
func (r *RuntimeClassRegistry) List() []*RuntimeClass
```

## Design Decisions Required

None. Design doc (docs/design/agentd/agentd.md) fully specifies RuntimeClass structure.

## Constraints

1. **Command is REQUIRED** - registry must reject configs without Command
2. **Env substitution at load time** - `${VAR}` resolved via os.Getenv when registry is created
3. **Thread-safe** - sync.RWMutex pattern from ari.Registry
4. **No database persistence** - RuntimeClasses are config-only, not stored in meta.db

## Don't Hand-Roll

- Env substitution: use os.Expand or simple strings.Replace/os.Getenv pattern
- Thread-safe map: follow ari.Registry pattern exactly (sync.RWMutex, Lock/Unlock for write, RLock/RUnlock for read)

## Forward Intelligence

### What's Risky

- **Breaking existing config.go tests**: RuntimeClassConfig struct change may affect tests. Check test files before modifying.
- **Env substitution edge cases**: `${VAR}` with VAR not set → empty string or keep `${VAR}`? Design says substitute, so empty string is correct.

### What Changed

- Current RuntimeClassConfig has Image/Resources (container concepts) - not used in design. These should be removed.
- Design adds Capabilities struct with streaming/sessionLoad/concurrentSessions.

### What to Watch

- Process Manager (S05) will consume RuntimeClassRegistry to generate config.json
- Session Manager (S04) stores RuntimeClass name in session metadata
- The registry must be initialized early in daemon startup (after config load)

### Integration Points

```
daemon startup flow:
  1. ParseConfig(path) → Config with RuntimeClasses map
  2. NewRuntimeClassRegistry(cfg.RuntimeClasses) → Registry (validated + substituted)
  3. Registry passed to Process Manager (S05)
  
session/new flow (S05):
  1. Get runtimeClass name from request
  2. registry.Get(name) → RuntimeClass
  3. Merge RuntimeClass.Env + request.Env
  4. Build config.json with RuntimeClass.Command/Args + merged Env
```

## Skills Discovered

No additional skills needed - standard Go patterns (struct, map, mutex, os.Getenv).