package subcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zoumo/mass/superagent/pkg/core"
	"github.com/zoumo/mass/superagent/triggers"
)

const triggerShutdownTimeout = 20 * time.Second

type composeTrigger struct {
	kind    string
	config  []byte
	binding triggers.Descriptor
}

func (t composeTrigger) name() string { return "triggers." + t.kind }

func (t composeTrigger) command(ctx context.Context, executable, action, path, coreEndpoint string, environment []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, "triggers", t.kind, action, "--config", path, "--compose-core-endpoint", coreEndpoint)
	cmd.Stdin = bytes.NewReader(t.config)
	cmd.Env = environment
	return cmd
}

func (t composeTrigger) check(ctx context.Context, executable, path, coreEndpoint string, environment []string) error {
	// Use the CLI's Component.Check with the same environment as the collector,
	// including for any DWS CLI commands started by preflight.
	ctx, cancel := context.WithTimeout(ctx, componentStartupTimeout)
	defer cancel()
	cmd := t.command(ctx, executable, "check", path, coreEndpoint, environment)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
	cmd.Stdout = io.Discard
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s preflight: %w", t.name(), ctx.Err())
		}
		var failure struct {
			Error core.Error `json:"error"`
		}
		if json.Unmarshal(diagnostics.Bytes(), &failure) == nil && failure.Error.Message != "" {
			return fmt.Errorf("%s preflight: %s", t.name(), failure.Error.Message)
		}
		return fmt.Errorf("%s preflight: %w", t.name(), err)
	}
	return nil
}

func prepareComposeTriggers(path string, sections map[string][]byte, config core.Config) ([]composeTrigger, error) {
	if len(sections) == 0 {
		return nil, nil
	}
	coreHost, corePort, _ := net.SplitHostPort(config.Listen) // Validated by Core.
	probeHost := coreHost
	if probeHost == "" {
		probeHost = "::"
	}
	probeURL := url.URL{Scheme: "http", Host: net.JoinHostPort(probeHost, "1")}
	probe := childHTTPOrigin(probeURL.String(), coreHost)
	if probe == nil {
		return nil, errors.New("core configuration: cannot determine the Trigger connection origin")
	}
	var configured []composeTrigger
	statePaths := make(map[string]string)
	if err := reserveComposePath(statePaths, path, "Compose configuration"); err != nil {
		return nil, err
	}
	if err := reserveComposePath(statePaths, config.DataDir, "core dataDir"); err != nil {
		return nil, err
	}
	statusListeners := make(map[string]string)
	for _, kind := range []string{triggers.GitHub, triggers.DWS} {
		section, enabled := sections[kind]
		if !enabled {
			continue
		}
		t := composeTrigger{kind: kind, config: section}
		component, err := triggers.Load(kind, path, bytes.NewReader(section), probe.String())
		if err != nil {
			return nil, fmt.Errorf("%s configuration (using core.listen): %w", t.name(), err)
		}
		t.binding = component.Describe()
		if err := t.reserveState(statePaths); err != nil {
			return nil, err
		}
		if err := t.reserveStatus(coreHost, corePort, statusListeners); err != nil {
			return nil, err
		}
		configured = append(configured, t)
	}
	for _, native := range configured {
		for _, adapter := range configured {
			if native.binding.UpstreamTokenEnv == adapter.binding.CoreTokenEnv {
				return nil, fmt.Errorf("%s githubTokenEnv conflicts with %s coreTokenEnv", native.name(), adapter.name())
			}
		}
	}
	return configured, nil
}

func (t composeTrigger) validateUpstreamToken(config core.Config, triggerToken string) error {
	if t.binding.UpstreamTokenEnv == "" {
		return nil
	}
	token := os.Getenv(t.binding.UpstreamTokenEnv)
	if token == "" {
		return errors.New("githubTokenEnv must reference a nonempty GitHub credential")
	}
	return config.CheckUpstreamToken(token, triggerToken)
}

func (t composeTrigger) reserveState(paths map[string]string) error {
	for _, suffix := range []string{"", ".lock", "-wal", "-shm", "-journal"} {
		if err := reserveComposePath(paths, t.binding.DatabasePath+suffix, t.name()+" databasePath"); err != nil {
			return err
		}
	}
	if t.kind == triggers.DWS {
		if err := reserveComposePath(paths, t.binding.DatabasePath+".dws-sends", t.name()+" send evidence"); err != nil {
			return err
		}
	}
	if t.binding.FeedbackPath != "" {
		for _, suffix := range []string{"", ".lock"} {
			if err := reserveComposePath(paths, t.binding.FeedbackPath+suffix, t.name()+" feedback.statePath"); err != nil {
				return err
			}
		}
	}
	return nil
}

func reserveComposePath(paths map[string]string, path, field string) error {
	canonical, err := resolveComposePath(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("%s: cannot resolve the configured path", field)
	}
	separator := string(filepath.Separator)
	for reserved, owner := range paths {
		if canonical == reserved || strings.HasPrefix(canonical, strings.TrimSuffix(reserved, separator)+separator) ||
			strings.HasPrefix(reserved, strings.TrimSuffix(canonical, separator)+separator) {
			return fmt.Errorf("%s conflicts with %s", field, owner)
		}
	}
	paths[canonical] = field
	return nil
}

// Resolve existing symlinks even when the configured state does not yet exist.
func resolveComposePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	parent := filepath.Dir(path)
	if !errors.Is(err, os.ErrNotExist) || parent == path {
		return resolved, err
	}
	resolved, err = resolveComposePath(parent)
	return filepath.Join(resolved, filepath.Base(path)), err
}

func (t composeTrigger) reserveStatus(coreHost, corePort string, statusListeners map[string]string) error {
	host, port, _ := net.SplitHostPort(t.binding.StatusListen) // Host validated by the Trigger.
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Errorf("%s statusListen requires a numeric port", t.name())
	}
	if number == 0 {
		return nil
	}
	coreNumber, _ := strconv.ParseUint(corePort, 10, 16)
	if number == coreNumber && overlappingListenHosts(host, coreHost) {
		return fmt.Errorf("%s statusListen conflicts with core.listen", t.name())
	}
	port = strconv.FormatUint(number, 10)
	for endpoint, other := range statusListeners {
		otherHost, otherPort, _ := net.SplitHostPort(endpoint)
		if port == otherPort && overlappingListenHosts(host, otherHost) {
			return fmt.Errorf("%s statusListen conflicts with %s", t.name(), other)
		}
	}
	statusListeners[net.JoinHostPort(host, port)] = t.name()
	return nil
}

func overlappingListenHosts(a, b string) bool {
	if a == "" || b == "" || strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, ".")) {
		return true
	}
	first, second := net.ParseIP(a), net.ParseIP(b)
	if first == nil || second == nil {
		// The OS resolves DNS names when binding the listeners.
		return false
	}
	return first.Equal(second) ||
		(first.IsUnspecified() && (first.To4() == nil || second.To4() != nil)) ||
		(second.IsUnspecified() && (second.To4() == nil || first.To4() != nil))
}

func composeChildEnvironment(environment []string, configured []composeTrigger, kind, triggerToken string) []string {
	filtered := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		scoped := false
		for _, t := range configured {
			if name == t.binding.CoreTokenEnv || (t.kind != kind && name == t.binding.UpstreamTokenEnv) {
				scoped = true
				break
			}
		}
		if !scoped {
			filtered = append(filtered, item)
		}
	}
	for _, t := range configured {
		if t.kind == kind {
			return append(filtered, t.binding.CoreTokenEnv+"="+triggerToken)
		}
	}
	return filtered
}

func triggerStartupEndpoint(line []byte, listen string) (*url.URL, error) {
	var record struct {
		Endpoint string `json:"statusEndpoint"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(line, &record); err != nil || record.Status != "starting" {
		return nil, errors.New("invalid Trigger startup record")
	}
	host, _, _ := net.SplitHostPort(listen)
	endpoint := childHTTPOrigin(record.Endpoint, host)
	if endpoint == nil {
		return nil, errors.New("invalid Trigger status endpoint")
	}
	return endpoint, nil
}

func stopComposeTriggers(children []*childProcess, startupComplete bool) error {
	var result error
	if !startupComplete {
		for _, child := range slices.Backward(children) {
			result = errors.Join(result, child.stop(triggerShutdownTimeout))
		}
		return result
	}
	failures := make([]error, len(children))
	var wait sync.WaitGroup
	for i, child := range children {
		wait.Go(func() {
			failures[i] = child.stop(triggerShutdownTimeout)
		})
	}
	wait.Wait()
	return errors.Join(failures...)
}
