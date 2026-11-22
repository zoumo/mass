package subcommand

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const (
	componentStartupTimeout = 30 * time.Second
	massdShutdownTimeout    = 30 * time.Second
	coreShutdownTimeout     = 20 * time.Second
)

func newCompose() *cobra.Command {
	var massdBin, config string
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Run massd, Superagent Core and enabled triggers together",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCompose(cmd, massdBin, config)
		},
	}
	cmd.Flags().StringVar(&massdBin, "massd-bin", "massd", "massd executable (defaults to massd on PATH)")
	cmd.Flags().StringVar(&config, "config", "", "Compose YAML configuration file")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func runCompose(cmd *cobra.Command, massdBin, path string) (result error) {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	config, err := loadComposeConfig(path)
	if err != nil {
		return err
	}
	coreConfig, err := core.LoadComposeConfig(path, bytes.NewReader(config.core))
	if err != nil {
		return errors.New("core configuration: " + err.Error())
	}
	triggers, probeEndpoint, err := prepareComposeTriggers(path, config.triggers, coreConfig)
	if err != nil {
		return err
	}
	superagent, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find the Superagent executable: %w", err)
	}
	environment := os.Environ()
	for _, trigger := range triggers {
		err := trigger.check(ctx, superagent, path, probeEndpoint, composeChildEnvironment(environment, triggers, trigger.kind))
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	sharedEnvironment := composeChildEnvironment(environment, triggers, "")
	diagnostics := cmd.ErrOrStderr()
	if _, ok := diagnostics.(*os.File); !ok {
		diagnostics = &composeDiagnostics{output: diagnostics}
	}

	// The supervisor signals and reaps each child in dependency order.
	massdCommand := exec.CommandContext(context.Background(), massdBin, "--config", path, "--compose-config-stdin")
	massdCommand.Stdin, massdCommand.Stderr = bytes.NewReader(config.massd), diagnostics
	massdCommand.Env = sharedEnvironment
	daemon, err := startChild("massd", massdCommand)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, daemon.stop(massdShutdownTimeout))
	}()
	massdEndpoint, err := awaitStartup(ctx, daemon, massdStartupEndpoint, "/readyz")
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return err
	}

	coreCommand := exec.CommandContext(context.Background(), superagent, "serve", "--config", path, "--compose-massd-endpoint", massdEndpoint)
	coreCommand.Stdin, coreCommand.Stderr = bytes.NewReader(config.core), diagnostics
	coreCommand.Env = sharedEnvironment
	server, err := startChild("core", coreCommand)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, server.stop(coreShutdownTimeout))
	}()
	parseCoreEndpoint := func(line []byte) (*url.URL, error) {
		return coreStartupEndpoint(line, coreConfig.Listen)
	}
	coreEndpoint, err := awaitStartup(ctx, server, parseCoreEndpoint, "/v1/observer/health", daemon)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return err
	}

	addresses := composeStartup{
		Endpoint:         coreEndpoint,
		ObserverEndpoint: coreEndpoint + "/observer/",
		MassdEndpoint:    massdEndpoint,
		TriggerEndpoints: make(map[string]string, len(triggers)),
		Status:           "ready",
	}
	children := []*childProcess{daemon, server}
	var triggerChildren []*childProcess
	startupComplete := false
	defer func() {
		result = errors.Join(result, stopComposeTriggers(triggerChildren, startupComplete))
	}()
	for _, trigger := range triggers {
		if ctx.Err() != nil {
			return nil
		}
		if err := runningComposeChildren(children); err != nil {
			return err
		}
		triggerCommand := trigger.command(context.Background(), superagent, "serve", path, coreEndpoint, composeChildEnvironment(environment, triggers, trigger.kind))
		triggerCommand.Stderr = diagnostics
		child, err := startChild(trigger.name(), triggerCommand)
		if err != nil {
			return err
		}
		child.diagnostics = diagnostics
		triggerChildren = append(triggerChildren, child)
		endpoint, err := awaitStartup(ctx, child, triggerStartupEndpoint, "/status", children...)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		addresses.TriggerEndpoints[trigger.kind] = endpoint
		children = append(children, child)
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := runningComposeChildren(children); err != nil {
		return err
	}
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(addresses); err != nil {
		return err
	}
	startupComplete = true
	logger := slog.New(slog.NewJSONHandler(diagnostics, nil))
	return observeCompose(ctx, logger, daemon, server, triggerChildren)
}

type composeStartup struct {
	Endpoint         string            `json:"endpoint"`
	ObserverEndpoint string            `json:"observerEndpoint"`
	MassdEndpoint    string            `json:"massdEndpoint"`
	Status           string            `json:"status"`
	TriggerEndpoints map[string]string `json:"triggerEndpoints,omitempty"`
}

type composeDiagnostics struct {
	mu     sync.Mutex
	output io.Writer
}

func (d *composeDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.output.Write(p)
}

func runningComposeChildren(children []*childProcess) error {
	for _, child := range children {
		select {
		case <-child.done:
			return child.unexpectedExit()
		default:
		}
	}
	return nil
}

func observeCompose(ctx context.Context, logger *slog.Logger, daemon, server *childProcess, triggers []*childProcess) error {
	exits := make(chan *childProcess, len(triggers))
	for _, child := range triggers {
		go func() {
			<-child.done
			exits <- child
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-server.done:
			return server.unexpectedExit()
		case <-daemon.done:
			return daemon.unexpectedExit()
		case child := <-exits:
			logger.Error("Trigger exited", "component", child.name, "error", child.unexpectedExit())
		}
	}
}

type childProcess struct {
	name        string
	cmd         *exec.Cmd
	stdout      *os.File
	diagnostics io.Writer
	readDone    chan struct{}
	done        chan struct{}
	waitErr     error // Published by closing done.
}

func startChild(name string, cmd *exec.Cmd) (*childProcess, error) {
	stdout, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("cannot create %s startup pipe: %w", name, err)
	}
	defer writer.Close()
	cmd.Stdout = writer
	cmd.WaitDelay = time.Second
	// Compose receives terminal Ctrl-C and stops its children in dependency order.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("cannot start %s: %w", name, err)
	}
	child := &childProcess{name: name, cmd: cmd, stdout: stdout, done: make(chan struct{})}
	go func() {
		defer close(child.done)
		child.waitErr = cmd.Wait()
	}()
	return child, nil
}

func (d *childProcess) unexpectedExit() error {
	if d.waitErr != nil {
		return fmt.Errorf("%s exited unexpectedly: %w", d.name, d.waitErr)
	}
	return fmt.Errorf("%s exited unexpectedly", d.name)
}

func (d *childProcess) stop(grace time.Duration) error {
	defer func() {
		if d.readDone != nil && d.diagnostics != nil {
			select {
			case <-d.readDone:
			case <-time.After(time.Second):
			}
		}
		_ = d.stdout.Close()
	}()
	select {
	case <-d.done:
		return nil
	default:
	}
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("cannot stop %s: %w", d.name, err)
	}
	timeout := time.NewTimer(grace)
	defer timeout.Stop()
	select {
	case <-d.done:
		var exit *exec.ExitError
		if errors.As(d.waitErr, &exit) && exit.Sys().(syscall.WaitStatus).Signal() == syscall.SIGTERM {
			// A child interrupted before installing signal handlers exits directly.
			return nil
		}
		if d.waitErr != nil {
			return fmt.Errorf("%s shutdown incomplete: %w", d.name, d.waitErr)
		}
		return nil
	case <-timeout.C:
		if err := d.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("cannot kill %s after shutdown timeout: %w", d.name, err)
		}
		<-d.done
		return fmt.Errorf("%s exceeded shutdown timeout", d.name)
	}
}

func awaitStartup(ctx context.Context, child *childProcess, parse func([]byte) (*url.URL, error), healthPath string, dependencies ...*childProcess) (string, error) {
	startup, cancel := context.WithTimeout(ctx, componentStartupTimeout)
	defer cancel()
	return child.waitReady(startup, parse, healthPath, dependencies...)
}

func (d *childProcess) waitReady(ctx context.Context, parse func([]byte) (*url.URL, error), healthPath string, dependencies ...*childProcess) (string, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	for _, dependency := range dependencies {
		go func() {
			select {
			case <-ctx.Done():
			case <-dependency.done:
				cancel(dependency.unexpectedExit())
			}
		}()
	}

	type startupRecord struct {
		endpoint *url.URL
		err      error
	}
	startup := make(chan startupRecord, 1)
	d.readDone = make(chan struct{})
	go func() {
		defer close(d.readDone)
		reader := bufio.NewReader(d.stdout)
		line, err := reader.ReadSlice('\n')
		if err != nil {
			startup <- startupRecord{err: fmt.Errorf("%s did not write a complete startup record", d.name)}
		} else {
			endpoint, err := parse(line)
			if err != nil {
				err = fmt.Errorf("%s: %w", d.name, err)
			}
			startup <- startupRecord{endpoint, err}
		}
		output := d.diagnostics
		if output == nil {
			output = io.Discard
		}
		_, _ = io.Copy(output, reader)
	}()

	var endpoint *url.URL
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("waiting for %s startup: %w", d.name, context.Cause(ctx))
	case <-d.done:
		return "", d.unexpectedExit()
	case record := <-startup:
		if record.err != nil {
			return "", record.err
		}
		endpoint = record.endpoint
	}

	probe := *endpoint
	probe.Path = healthPath
	switch probe.Scheme {
	case "ws":
		probe.Scheme = "http"
	case "wss":
		probe.Scheme = "https"
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.String(), http.NoBody)
	if err != nil {
		return "", err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				select {
				case <-d.done:
					return "", d.unexpectedExit()
				default:
				}
				if err := runningComposeChildren(dependencies); err != nil {
					return "", err
				}
				return endpoint.String(), nil
			}
		}
		select {
		case <-d.done:
			return "", d.unexpectedExit()
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for %s readiness: %w", d.name, context.Cause(ctx))
		case <-ticker.C:
		}
	}
}

func massdStartupEndpoint(line []byte) (*url.URL, error) {
	var record struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(line, &record); err != nil {
		return nil, errors.New("invalid massd startup record")
	}
	endpoint := childEndpoint(record.URL, rpcwire.WebSocketPath, "ws", "wss")
	if endpoint == nil {
		return nil, errors.New("invalid massd startup RPC endpoint")
	}
	return endpoint, nil
}

func coreStartupEndpoint(line []byte, listen string) (*url.URL, error) {
	var record struct {
		Endpoint string `json:"endpoint"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(line, &record); err != nil || record.Status != "ready" {
		return nil, errors.New("invalid core startup record")
	}
	endpoint := childEndpoint(record.Endpoint, "", "http")
	if endpoint == nil {
		return nil, errors.New("invalid core startup HTTP endpoint")
	}
	host, _, _ := net.SplitHostPort(listen)
	// Go can bind an IPv4 wildcard as an IPv6 dual-stack socket. Preserve
	// the configured address family while connecting to the actual port.
	if net.ParseIP(host).Equal(net.IPv4zero) && record.Endpoint == "http://[::]:"+endpoint.Port() {
		endpoint.Host = net.JoinHostPort("127.0.0.1", endpoint.Port())
	}
	return endpoint, nil
}

func childEndpoint(raw, path string, schemes ...string) *url.URL {
	endpoint, err := url.Parse(raw)
	if err != nil || !slices.Contains(schemes, endpoint.Scheme) || endpoint.User != nil ||
		endpoint.Path != path || endpoint.RawPath != "" || endpoint.ForceQuery || endpoint.RawQuery != "" || strings.Contains(raw, "#") {
		return nil
	}
	address := net.ParseIP(endpoint.Hostname())
	port, err := strconv.ParseUint(endpoint.Port(), 10, 16)
	if address == nil || err != nil || port == 0 {
		return nil
	}
	// Connect to the matching loopback address when the listener is a wildcard.
	if address.IsUnspecified() {
		host := "::1"
		if address.To4() != nil {
			host = "127.0.0.1"
		}
		endpoint.Host = net.JoinHostPort(host, endpoint.Port())
	}
	return endpoint
}
