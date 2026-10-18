// Package catalog assembles the system-provided protocol implementations.
// Registrations select configuration; they cannot supply executable Go plugins.
package catalog

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zoumo/mass/mass-go/internal/managed"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
)

type Catalog struct {
	bindings rt.BindingStore
	mu       sync.Mutex
	runtimes map[string]*managed.Runtime
}

func New() *Catalog { return NewWithBindings(nil) }
func NewWithBindings(bindings rt.BindingStore) *Catalog {
	return &Catalog{bindings: bindings, runtimes: make(map[string]*managed.Runtime)}
}

const maxNameBytes = 128

func (c *Catalog) Types() []rt.RuntimeType {
	types := []rt.RuntimeType{{Name: "codex", SchemaVersion: 1, Topology: "shared_local_process"}, {Name: "claude", SchemaVersion: 1, Topology: "per_session_process"}}
	for i := range types {
		types[i].MaxNameBytes = maxNameBytes
		types[i].DeliveryBounds = []rt.LimitField{{Name: "facts", Default: 256, Min: 1, Max: 4096}, {Name: "bytes", Default: 4 << 20, Min: 1, Max: 16 << 20}}
		types[i].CreationBounds = []rt.LimitField{{Name: "facts", Default: 64, Min: 1, Max: 4096}, {Name: "bytes", Default: 1 << 20, Min: 1, Max: 16 << 20}, {Name: "timeoutNanoseconds", Default: int64(30 * time.Second), Min: int64(time.Millisecond), Max: int64(60 * time.Second)}}
		types[i].ConfigFields = []rt.ConfigField{{Name: "executable", Type: "string", Required: true, AbsolutePath: true}, {Name: "env", Type: "string_array", Required: true}, {Name: "dir", Type: "string", AbsolutePath: true}, {Name: "deliveryLimits", Type: "object"}, {Name: "creationLimits", Type: "object"}}
	}
	return types
}
func (c *Catalog) Describe(name string) (rt.RuntimeType, error) {
	for _, kind := range c.Types() {
		if kind.Name == name {
			return kind, nil
		}
	}
	return rt.RuntimeType{}, rt.ErrNotFound
}
func valid(reg rt.Registration) bool {
	delivery := reg.Config.DeliveryLimits.Effective()
	if delivery.Facts < 1 || delivery.Facts > 4096 || delivery.Bytes < 1 || delivery.Bytes > 16<<20 || len(reg.Config.Env) > 256 {
		return false
	}
	configBytes := len(reg.Config.Executable) + len(reg.Config.Dir)
	for _, v := range reg.Config.Env {
		configBytes += len(v)
	}
	if configBytes > 1<<20 {
		return false
	}
	limits := reg.Config.CreationLimits.Effective()
	if limits.Facts < 1 || limits.Facts > 4096 || limits.Bytes < 1 || limits.Bytes > 16<<20 || limits.Timeout < time.Millisecond || limits.Timeout > 60*time.Second {
		return false
	}
	if reg.Name == "" || len(reg.Name) > maxNameBytes || strings.TrimSpace(reg.Name) != reg.Name || strings.ContainsRune(reg.Name, 0) {
		return false
	}
	if reg.Type != "codex" && reg.Type != "claude" {
		return false
	}
	if !filepath.IsAbs(reg.Config.Executable) || strings.ContainsRune(reg.Config.Executable, 0) || reg.Config.Env == nil {
		return false
	}
	if reg.Config.Dir != "" && (!filepath.IsAbs(reg.Config.Dir) || strings.ContainsRune(reg.Config.Dir, 0)) {
		return false
	}
	for _, entry := range reg.Config.Env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) {
			return false
		}
	}
	return true
}
func (c *Catalog) construct(reg rt.Registration, revision uint64) *managed.Runtime {
	if reg.Type == "codex" {
		return codex.NewWithBindings(reg.Name, reg.Config, revision, c.bindings)
	}
	return claude.NewWithBindings(reg.Name, reg.Config, revision, c.bindings)
}
func (c *Catalog) Register(reg rt.Registration) (rt.Snapshot, error) {
	if !valid(reg) {
		return rt.Snapshot{}, rt.ErrInvalidInput
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.runtimes[reg.Name]; exists {
		return rt.Snapshot{}, rt.ErrConflict
	}
	if len(c.runtimes) >= 256 {
		return rt.Snapshot{}, rt.ErrBackpressure
	}
	runtime := c.construct(reg, 1)
	c.runtimes[reg.Name] = runtime
	return runtime.Status(), nil
}
func (c *Catalog) Get(name string) (rt.Runtime, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	runtime, ok := c.runtimes[name]
	if !ok {
		return nil, rt.ErrNotFound
	}
	return runtime, nil
}
func (c *Catalog) List() []rt.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshots := make([]rt.Snapshot, 0, len(c.runtimes))
	for _, runtime := range c.runtimes {
		snapshots = append(snapshots, runtime.Status())
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Name < snapshots[j].Name })
	return snapshots
}
func (c *Catalog) UpdateConfig(name string, config rt.Config, expectedRevision uint64) (rt.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	runtime, ok := c.runtimes[name]
	if !ok {
		return rt.Snapshot{}, rt.ErrNotFound
	}
	snapshot := runtime.Status()
	reg := rt.Registration{Name: name, Type: snapshot.Type, Config: config}
	if !valid(reg) {
		return rt.Snapshot{}, rt.ErrInvalidInput
	}
	if snapshot.ConfigRevision != expectedRevision {
		return rt.Snapshot{}, rt.ErrConflict
	}
	// Retire the original handle atomically before replacing its configuration.
	if !runtime.Retire() {
		return rt.Snapshot{}, rt.ErrConflict
	}
	replacement := c.construct(reg, expectedRevision+1)
	c.runtimes[name] = replacement
	return replacement.Status(), nil
}
func (c *Catalog) Unregister(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	runtime, ok := c.runtimes[name]
	if !ok {
		return rt.ErrNotFound
	}
	if !runtime.Retire() {
		return rt.ErrConflict
	}
	delete(c.runtimes, name)
	return nil
}
