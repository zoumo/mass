// Package runtime owns execution values and the single-consumer Driver contract.
// It has no dependency on persistent resources, Store, or Service.
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"maps"
	"sync"
)

func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

var ErrResourcesReleased = errors.New("owned resources released")

var ErrWorkUnsettled = errors.New("work unsettled")

type (
	Config       map[string]any
	Capabilities map[string]bool
	SessionState string
)

const (
	Unknown        SessionState = "unknown"
	Idle           SessionState = "idle"
	Running        SessionState = "running"
	RequiresAction SessionState = "requires_action"
)

type Address struct {
	Namespace   string `json:"namespace"`
	SessionName string `json:"sessionName"`
}
type SessionSpec struct {
	Cwd            string   `json:"cwd"`
	SystemPrompt   string   `json:"systemPrompt,omitempty"`
	AdditionalDirs []string `json:"additionalDirs,omitempty"`
}

// Source is execution-only association. It never enters event JSON/history.
// A run token is fixed at open; work is the original native turn identity.
type Source struct{ run, work string }

func NewSource() Source                      { return Source{run: UUID()} }
func (s Source) WithWork(work string) Source { return Source{run: s.run, work: work} }
func (s Source) SameRun(other Source) bool   { return s.run != "" && s.run == other.run }
func (s Source) Work() string                { return s.work }

type DriverSession struct {
	SessionID    string
	Capabilities Capabilities
	Source       Source
}

type Input struct {
	Content []ContentBlock `json:"content"`
}
type Acceptance struct {
	Sent           *bool `json:"sent,omitempty"`
	NativeAccepted *bool `json:"nativeAccepted,omitempty"`
	NativeRecorded *bool `json:"nativeRecorded,omitempty"`
}

func Yes() *bool { v := true; return &v }

type InputReceipt struct {
	Submission string     `json:"submission"`
	Acceptance Acceptance `json:"acceptance"`
}
type InteractionReceipt struct {
	Interaction string     `json:"interaction"`
	Evidence    Acceptance `json:"evidence"`
}
type CancelReceipt struct {
	Target string `json:"target,omitempty"`
	Result string `json:"result"`
}
type Stream interface {
	Next(context.Context) (SessionEvent, error)
}
type Driver interface {
	Events() (Stream, error)
	Start(context.Context) (Capabilities, error)
	OpenSession(context.Context, Address, SessionSpec) (DriverSession, error)
	ResumeSession(context.Context, string, Address, SessionSpec) (DriverSession, error)
	SendPrompt(context.Context, string, Input) (InputReceipt, error)
	Cancel(context.Context, string) (CancelReceipt, error)
	AnswerInteraction(context.Context, string, string, any) (Acceptance, error)
	CloseSession(context.Context, string) error
	Stop(context.Context) error
}
type Factory interface {
	Validate(Config) error
	New(string, Config) (Driver, error)
}

// Registry manages only type factories and unstarted/current Driver mappings.
type Registry struct {
	mu        sync.Mutex
	factories map[string]Factory
	drivers   map[string]Driver
}

func NewRegistry(factories map[string]Factory) *Registry {
	copied := map[string]Factory{}
	maps.Copy(copied, factories)
	return &Registry{factories: copied, drivers: map[string]Driver{}}
}

func (r *Registry) ValidateConfig(kind string, c Config) error {
	r.mu.Lock()
	f := r.factories[kind]
	r.mu.Unlock()
	if f == nil {
		return InvalidArgument("unsupported runtime type")
	}
	if err := f.Validate(c); err != nil {
		return errors.Join(ErrInvalidArgument, err)
	}
	return nil
}

func (r *Registry) Create(kind, name string, c Config) (Driver, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drivers[name] != nil {
		return nil, errors.New("driver already registered")
	}
	f := r.factories[kind]
	if f == nil {
		return nil, errors.New("unsupported runtime type")
	}
	if err := f.Validate(c); err != nil {
		return nil, err
	}
	d, err := f.New(name, c)
	if err == nil {
		r.drivers[name] = d
	}
	return d, err
}

func (r *Registry) Get(name string) (Driver, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.drivers[name]
	if d == nil {
		return nil, errors.New("driver not registered")
	}
	return d, nil
}
func (r *Registry) Remove(name string) { r.mu.Lock(); defer r.mu.Unlock(); delete(r.drivers, name) }
