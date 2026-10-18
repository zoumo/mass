// Package catalog assembles the system-provided protocol implementations.
// Registrations select configuration; they cannot supply executable Go plugins.
package catalog

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/zoumo/mass/mass-go/internal/managed"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
)

type Catalog struct {
	mu       sync.Mutex
	runtimes map[string]*managed.Runtime
}

func New() *Catalog { return &Catalog{runtimes: make(map[string]*managed.Runtime)} }

const maxNameBytes = 128

func (c *Catalog) Types() []rt.RuntimeType {
	types := []rt.RuntimeType{{Name: "codex", SchemaVersion: 1, Topology: "shared_local_process"}, {Name: "claude", SchemaVersion: 1, Topology: "per_session_process"}}
	for i := range types {
		types[i].MaxNameBytes = maxNameBytes
		types[i].ConfigFields = []rt.ConfigField{{Name: "executable", Type: "string", Required: true, AbsolutePath: true}, {Name: "env", Type: "string_array", Required: true}, {Name: "dir", Type: "string", AbsolutePath: true}}
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
func construct(reg rt.Registration, revision uint64) *managed.Runtime {
	if reg.Type == "codex" {
		return codex.New(reg.Name, reg.Config, revision)
	}
	return claude.New(reg.Name, reg.Config, revision)
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
	runtime := construct(reg, 1)
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
	replacement := construct(reg, expectedRevision+1)
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
