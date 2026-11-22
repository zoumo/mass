package triggers

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"github.com/zoumo/mass/superagent/triggers/dws"
	"github.com/zoumo/mass/superagent/triggers/github"
	"github.com/zoumo/mass/superagent/triggers/internal/trigger"
)

const (
	DWS    = "dws"
	GitHub = "github"
)

// Descriptor exposes the configuration Compose needs to check bindings and
// isolate environment variables. It contains credential names only.
type Descriptor = trigger.Descriptor

type adapter interface {
	Describe() Descriptor
	Check(context.Context, io.Writer) error
	Serve(context.Context, io.Writer) error
}

// Component holds a validated Trigger configuration without reading credentials
// or contacting an upstream service.
type Component struct{ adapter adapter }

// Load reads a standalone configuration file, or a Compose section with a
// supervisor-supplied Core endpoint. Relative paths in a section use path's
// directory as their base. Standalone configurations use absolute paths.
func Load(kind, path string, section io.Reader, coreEndpoint string) (Component, error) {
	var err error
	if section != nil {
		path, err = filepath.Abs(path)
		if err != nil {
			return Component{}, errors.New("cannot resolve configuration path")
		}
	}
	switch kind {
	case DWS:
		c, err := dws.Load(path, section, coreEndpoint)
		return Component{adapter: c}, err
	case GitHub:
		c, err := github.Load(path, section, coreEndpoint)
		return Component{adapter: c}, err
	default:
		return Component{}, errors.New("unknown Trigger")
	}
}

func (c Component) selected() adapter {
	if c.adapter == nil {
		return dws.Component{}
	}
	return c.adapter
}

func (c Component) Describe() Descriptor { return c.selected().Describe() }

// Check verifies the configured credentials, state paths and local command contract.
// State ownership and exclusive lock acquisition are checked when Serve opens the state.
func (c Component) Check(ctx context.Context, out io.Writer) error {
	return c.selected().Check(ctx, out)
}

func (c Component) Serve(ctx context.Context, out io.Writer) error {
	return c.selected().Serve(ctx, out)
}

// Status reads the selected Trigger's local status endpoint.
func Status(ctx context.Context, kind, endpoint string, out io.Writer) error {
	if kind == GitHub {
		return github.Status(ctx, endpoint, out)
	}
	return dws.Status(ctx, endpoint, out)
}
