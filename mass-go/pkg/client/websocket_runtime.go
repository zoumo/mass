package client

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

type runtimeCreateParams struct {
	Input runtimeCreateInput `json:"input"`
}
type runtimeCreateInput struct {
	Metadata runtimeCreateMetadata `json:"metadata"`
	Spec     resource.RuntimeSpec  `json:"spec"`
}
type runtimeCreateMetadata struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}
type runtimeRefParams struct {
	RuntimeRef string `json:"runtimeRef"`
}
type runtimeUpdateParams struct {
	RuntimeRef string               `json:"runtimeRef"`
	Spec       resource.RuntimeSpec `json:"spec"`
}
type runtimeListParams struct {
	Query *resource.RuntimeQuery `json:"query,omitempty"`
}

// CreateRuntime registers Runtime metadata and spec without starting a Driver.
func (c *Client) CreateRuntime(ctx context.Context, v resource.Runtime, out any) error {
	if err := events.CheckDynamicJSON(map[string]any(v.Spec.Config)); err != nil {
		return invalid()
	}
	params := runtimeCreateParams{Input: runtimeCreateInput{
		Metadata: runtimeCreateMetadata{Namespace: v.Metadata.Namespace, Name: v.Metadata.Name},
		Spec:     v.Spec,
	}}
	return c.call(ctx, "runtime.create", params, out)
}

// GetRuntime returns one persistent Runtime without side effects.
func (c *Client) GetRuntime(ctx context.Context, name string, out any) error {
	if !validSegment(name) {
		return invalid()
	}
	return c.call(ctx, "runtime.get", runtimeRefParams{RuntimeRef: name}, out)
}

// ListRuntimes returns all registered Runtimes matching the optional query.
func (c *Client) ListRuntimes(ctx context.Context, q resource.RuntimeQuery, out any) error {
	params := runtimeListParams{}
	if q != (resource.RuntimeQuery{}) {
		params.Query = &q
	}
	return c.call(ctx, "runtime.list", params, out)
}

// UpdateRuntime replaces the complete desired spec.
func (c *Client) UpdateRuntime(ctx context.Context, name string, spec resource.RuntimeSpec, out any) error {
	if err := events.CheckDynamicJSON(map[string]any(spec.Config)); err != nil {
		return invalid()
	}
	if !validSegment(name) {
		return invalid()
	}
	return c.call(ctx, "runtime.update", runtimeUpdateParams{RuntimeRef: name, Spec: spec}, out)
}

// StartRuntime starts an unstarted Runtime and returns its current state.
func (c *Client) StartRuntime(ctx context.Context, name string, out any) error {
	if !validSegment(name) {
		return invalid()
	}
	return c.call(ctx, "runtime.start", runtimeRefParams{RuntimeRef: name}, out)
}

// StopRuntime waits for the Runtime's complete release and settlement.
func (c *Client) StopRuntime(ctx context.Context, name string, out any) error {
	if !validSegment(name) {
		return invalid()
	}
	return c.call(ctx, "runtime.stop", runtimeRefParams{RuntimeRef: name}, out)
}

// DeleteRuntime removes a stopped, unreferenced Runtime.
func (c *Client) DeleteRuntime(ctx context.Context, name string) error {
	if !validSegment(name) {
		return invalid()
	}
	return c.call(ctx, "runtime.delete", runtimeRefParams{RuntimeRef: name}, nil)
}

func decodeResult(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(out)
}
