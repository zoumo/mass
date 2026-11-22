package subcommand

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const (
	massdStartupTimeout  = 30 * time.Second
	massdShutdownTimeout = 25 * time.Second
)

func newCompose() *cobra.Command {
	var massdBin, massdConfig, coreConfig string
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Run massd and Superagent Core together",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCompose(cmd, massdBin, massdConfig, coreConfig)
		},
	}
	cmd.Flags().StringVar(&massdBin, "massd-bin", "massd", "massd executable (defaults to massd on PATH)")
	cmd.Flags().StringVar(&massdConfig, "massd-config", "", "massd YAML configuration file")
	cmd.Flags().StringVar(&coreConfig, "core-config", "", "Core YAML configuration file")
	_ = cmd.MarkFlagRequired("massd-config")
	_ = cmd.MarkFlagRequired("core-config")
	return cmd
}

func runCompose(cmd *cobra.Command, massdBin, massdConfig, coreConfig string) (result error) {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config, err := core.LoadComposeConfig(coreConfig)
	if err != nil {
		return err
	}
	daemon, err := startMassd(massdBin, massdConfig, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer func() {
		if err := daemon.stop(massdShutdownTimeout); err != nil {
			result = errors.Join(result, err)
		}
	}()

	startup, cancel := context.WithTimeout(ctx, massdStartupTimeout)
	defer cancel()
	endpoint, err := daemon.waitReady(startup)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	cancel()
	if ctx.Err() != nil {
		return nil
	}

	if config.Massd != nil {
		config.Massd.Endpoint = endpoint
	}
	return runCore(ctx, cmd, config, endpoint, daemon)
}

type massdProcess struct {
	cmd     *exec.Cmd
	stdout  *os.File
	done    chan struct{}
	waitErr error // Published by closing done.
}

func startMassd(binary, config string, stderr io.Writer) (*massdProcess, error) {
	stdout, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("cannot create massd startup pipe: %w", err)
	}
	defer writer.Close()
	// The supervisor stops and reaps massd after Core shuts down.
	cmd := exec.CommandContext(context.Background(), binary, "--config", config)
	cmd.Stdout = writer
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	// Keep terminal Ctrl-C from stopping massd before Core shuts down.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("cannot start massd: %w", err)
	}
	daemon := &massdProcess{cmd: cmd, stdout: stdout, done: make(chan struct{})}
	go func() {
		defer close(daemon.done)
		daemon.waitErr = cmd.Wait()
	}()
	return daemon, nil
}

func (d *massdProcess) unexpectedExit() error {
	if d.waitErr != nil {
		return fmt.Errorf("massd exited unexpectedly: %w", d.waitErr)
	}
	return errors.New("massd exited unexpectedly")
}

func (d *massdProcess) stop(grace time.Duration) error {
	defer func() { _ = d.stdout.Close() }()
	select {
	case <-d.done:
		return nil
	default:
	}
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("cannot stop massd: %w", err)
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
			return fmt.Errorf("massd shutdown incomplete: %w", d.waitErr)
		}
		return nil
	case <-timeout.C:
		if err := d.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("cannot kill massd after shutdown timeout: %w", err)
		}
		<-d.done
		return errors.New("massd exceeded shutdown timeout")
	}
}

func (d *massdProcess) waitReady(ctx context.Context) (string, error) {
	type startupRecord struct {
		endpoint *url.URL
		err      error
	}
	startup := make(chan startupRecord, 1)
	go func() {
		reader := bufio.NewReader(d.stdout)
		line, err := reader.ReadSlice('\n')
		if err != nil {
			startup <- startupRecord{err: errors.New("massd did not write a complete startup record")}
		} else {
			endpoint, err := massdStartupEndpoint(line)
			startup <- startupRecord{endpoint, err}
		}
		_, _ = io.Copy(io.Discard, reader)
	}()

	var endpoint *url.URL
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("waiting for massd startup: %w", ctx.Err())
	case <-d.done:
		return "", d.unexpectedExit()
	case record := <-startup:
		if record.err != nil {
			return "", record.err
		}
		endpoint = record.endpoint
	}

	probe := *endpoint
	probe.Scheme = "http"
	probe.Path = "/readyz"
	if endpoint.Scheme == "wss" {
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
				return endpoint.String(), nil
			}
		}
		select {
		case <-d.done:
			return "", d.unexpectedExit()
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for massd readiness: %w", ctx.Err())
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
	endpoint, err := url.Parse(record.URL)
	if err != nil || (endpoint.Scheme != "ws" && endpoint.Scheme != "wss") ||
		endpoint.User != nil || endpoint.Path != rpcwire.WebSocketPath || endpoint.RawPath != "" ||
		endpoint.ForceQuery || endpoint.RawQuery != "" || strings.Contains(record.URL, "#") {
		return nil, errors.New("invalid massd startup RPC endpoint")
	}
	address := net.ParseIP(endpoint.Hostname())
	port, err := strconv.ParseUint(endpoint.Port(), 10, 16)
	if address == nil || err != nil || port == 0 {
		return nil, errors.New("invalid massd startup RPC address")
	}
	// massd reports the listener address, which may be an IPv4 or IPv6 wildcard.
	if address.IsUnspecified() {
		host := "::1"
		if address.To4() != nil {
			host = "127.0.0.1"
		}
		endpoint.Host = net.JoinHostPort(host, endpoint.Port())
	}
	return endpoint, nil
}
