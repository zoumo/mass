// Package runtime owns execution values and the single-consumer Driver contract.
// It has no dependency on persistent resources, Store, or Service.
package runtime

import (
	"context"
	"errors"
	"maps"
	"sync"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

var ErrResourcesReleased = errors.New("owned resources released")

var ErrWorkUnsettled = errors.New("work unsettled")

type (
	Config       map[string]any
	Capabilities map[string]bool
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

type DriverSession struct {
	SessionID    string
	Capabilities Capabilities
	Source       events.Source
}

type Input struct {
	Content []events.ContentBlock `json:"content"`
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
type Driver interface {
	Events() (events.Stream, error)
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
