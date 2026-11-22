package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DWS    = "dws"
	GitHub = "github"
)

// Component holds a validated Trigger configuration without reading credentials
// or contacting an upstream service.
type Component struct {
	kind   string
	dws    Config
	github githubConfig
}

// Descriptor exposes the configuration Compose needs to check bindings and
// isolate environment variables. It contains credential names only.
type Descriptor struct {
	Source, AccountID, InstanceID  string
	CoreTokenEnv, UpstreamTokenEnv string
	StatusListen, DatabasePath     string
	FeedbackPath                   string
	OutboxConsumer                 bool
}

// Load reads a standalone configuration file, or a Compose section with a
// supervisor-supplied Core endpoint. Relative paths in a section use path's
// directory as their base. Standalone configurations use absolute paths.
func Load(kind, path string, section io.Reader, coreEndpoint string) (Component, error) {
	component := Component{kind: kind}
	var err error
	if section != nil {
		path, err = filepath.Abs(path)
		if err != nil {
			return component, errors.New("cannot resolve configuration path")
		}
	}
	switch kind {
	case DWS:
		component.dws, err = loadDWSConfig(path, section, coreEndpoint)
	case GitHub:
		component.github, err = loadGitHubConfig(path, section, coreEndpoint)
	default:
		err = errors.New("unknown Trigger")
	}
	return component, err
}

func resolveSectionPath(configPath, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(filepath.Dir(configPath), value)
}

func (c Component) Describe() Descriptor {
	if c.kind == GitHub {
		g := c.github
		return Descriptor{
			Source: githubSource, AccountID: g.AccountID, InstanceID: g.InstanceID,
			CoreTokenEnv: g.TokenEnv, UpstreamTokenEnv: g.GitHubTokenEnv,
			StatusListen: g.Listen, DatabasePath: g.DatabasePath,
		}
	}
	d := c.dws
	paths := Descriptor{
		Source: d.Source, AccountID: d.AccountID, InstanceID: d.InstanceID,
		CoreTokenEnv: d.TokenEnv, StatusListen: d.Listen, DatabasePath: d.DatabasePath,
		OutboxConsumer: d.repliesEnabled(),
	}
	if d.Feedback != nil {
		paths.FeedbackPath = d.Feedback.StatePath
	}
	return paths
}

// Check verifies the configured credentials, state paths and local command contract.
// State ownership and exclusive lock acquisition are checked when Serve opens the state.
func (c Component) Check(ctx context.Context, out io.Writer) error {
	paths := c.Describe()
	if err := checkStateParent(paths.DatabasePath); err != nil {
		return fmt.Errorf("databasePath: %w", err)
	}
	if err := checkTriggerDatabaseFile(paths.DatabasePath); err != nil {
		return err
	}
	if paths.FeedbackPath != "" {
		if err := checkStateParent(paths.FeedbackPath); err != nil {
			return fmt.Errorf("feedback.statePath: %w", err)
		}
		info, err := os.Stat(paths.FeedbackPath)
		if !errors.Is(err, os.ErrNotExist) && (err != nil || !info.Mode().IsRegular()) {
			return errors.New("feedback.statePath must be a regular file")
		}
	}
	_, _, result, err := c.preflight(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}

func checkStateParent(path string) error {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		switch {
		case err == nil && info.IsDir():
			return nil
		case !errors.Is(err, os.ErrNotExist), filepath.Dir(dir) == dir:
			return errors.New("cannot resolve the state directory")
		}
	}
}

func (c Component) Serve(ctx context.Context, out io.Writer) error {
	return c.serve(ctx, out, githubGraphQLEndpoint)
}

func (c Component) serve(ctx context.Context, out io.Writer, githubEndpoint string) error {
	core, token, _, err := c.preflight(ctx)
	if err != nil {
		return err
	}
	if c.kind == GitHub {
		api := newGitHubAPI(githubEndpoint, token, time.Duration(c.github.RequestMillis)*time.Millisecond)
		return runGitHub(ctx, c.github, api, core, out)
	}
	return run(ctx, c.dws, core, out)
}

func (c Component) preflight(ctx context.Context) (*CoreClient, string, any, error) {
	if c.kind == GitHub {
		g := c.github
		token, coreToken := os.Getenv(g.GitHubTokenEnv), os.Getenv(g.TokenEnv)
		if token == "" || strings.ContainsAny(token, " \t\r\n") || token == coreToken {
			return nil, "", nil, errors.New("GitHub credential reference must resolve to a non-whitespace token distinct from the Core token")
		}
		core, err := newCore(Config{CoreEndpoint: g.CoreEndpoint, RequestMillis: g.RequestMillis}, coreToken)
		return core, token, map[string]string{"status": "configuration_checked"}, err
	}
	core, err := newCore(c.dws, os.Getenv(c.dws.TokenEnv))
	if err != nil {
		return nil, "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	version, err := preflight(ctx, c.dws)
	return core, "", map[string]any{
		"status": "contract_checked", "version": version, "profileExplicit": true, "liveSubscriptionStarted": false,
	}, err
}

// Status reads the selected Trigger's local status endpoint.
func Status(ctx context.Context, kind, endpoint string, out io.Writer) error {
	if kind == GitHub {
		return githubStatusCommand(ctx, endpoint, out)
	}
	return status(ctx, endpoint, out)
}
