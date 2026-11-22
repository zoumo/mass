package subcommand

import (
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
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zoumo/mass/mass-go/pkg/massd"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const coreShutdownTimeout = 10 * time.Second

func Execute() int {
	cmd := New()
	if err := cmd.Execute(); err != nil {
		var problem *core.Error
		if !errors.As(err, &problem) {
			problem = &core.Error{Code: "invalid_argument", Message: err.Error(), Retryable: false}
		}
		if encodeErr := json.NewEncoder(cmd.ErrOrStderr()).Encode(map[string]any{"error": problem}); encodeErr != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "error output failed")
		}

		return 1
	}
	return 0
}

func New() *cobra.Command {
	root := &cobra.Command{Use: "superagent", Short: "Superagent Core and read-only observation", SilenceUsage: true, SilenceErrors: true}
	var config string
	var composed bool
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		signals, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var c core.Config
		var err error
		if composed {
			c, err = core.LoadComposeConfig(config, cmd.InOrStdin())
		} else {
			c, err = core.LoadConfig(config)
		}
		if err != nil {
			return err
		}
		return runCore(signals, cmd, c)
	}}
	serve.Flags().StringVar(&config, "config", "", "YAML configuration file")
	serve.Flags().BoolVar(&composed, "compose-config-stdin", false, "Read the Core configuration section from Compose")
	_ = serve.Flags().MarkHidden("compose-config-stdin")
	_ = serve.MarkFlagRequired("config")
	root.AddCommand(serve, newCompose(), newTriggers())
	var endpoint, cursor, thread, topic, issue, message, activityFilter string
	var limit int
	var newest bool
	observe := &cobra.Command{Use: "observe", Short: "Read Core snapshots without write credentials"}
	observe.PersistentFlags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:24370", "Core HTTP endpoint")
	observe.PersistentFlags().StringVar(&cursor, "cursor", "", "Opaque continuation cursor")
	observe.PersistentFlags().StringVar(&thread, "thread-key", "", "Filter Messages, subscriptions or Outbox by exact Thread key")
	observe.PersistentFlags().StringVar(&message, "message-id", "", "Filter Issue materials, Outbox, Router Messages, relations or routing events by exact Message ID")
	observe.PersistentFlags().StringVar(&topic, "topic-id", "", "Filter Issues, Timers, PR associations, subscriptions, relations, routing events or Outbox by Topic ID")
	observe.PersistentFlags().StringVar(&issue, "issue-id", "", "Filter SessionBindings, Reports, internal events or PR associations by exact Issue ID")
	observe.PersistentFlags().IntVar(&limit, "limit", 50, "Page size (1–200)")
	observe.PersistentFlags().BoolVar(&newest, "newest", false, "List newest records first")
	for _, kind := range []string{"messages", "threads", "internal-events", "topics", "issues", "reports", "timers", "pull-requests", "router-messages", "topic-activity", "routing-events", "subscriptions", "relations", "session-bindings", "health", "outbox"} {
		cmd := &cobra.Command{Use: kind + " [id]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			u, err := url.Parse(endpoint)
			if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Scheme != "http" {
				return &core.Error{Code: "invalid_argument", Message: "endpoint must be a plain HTTP origin"}
			}
			if net.ParseIP(u.Hostname()) == nil {
				return &core.Error{Code: "invalid_argument", Message: "endpoint must use a numeric IP address"}
			}

			path := "/v1/observer/" + kind
			if len(args) > 0 {
				path += "/" + url.PathEscape(args[0])
			}
			q := url.Values{}
			if kind == "topic-activity" && activityFilter != "" {
				q.Set("filter", activityFilter)
			}
			if cmd.Flags().Changed("limit") {
				q.Set("limit", strconv.Itoa(limit))
			}
			if newest {
				q.Set("order", "newest")
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if thread != "" {
				q.Set("threadKey", thread)
			}
			if message != "" {
				q.Set("messageId", message)
			}
			if topic != "" {
				q.Set("topicId", topic)
			}
			if issue != "" {
				q.Set("issueId", issue)
			}
			request, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, endpoint+path+"?"+q.Encode(), http.NoBody)
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				return &core.Error{Code: "unavailable", Message: "Core query failed", Retryable: true}
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				var body struct {
					Error core.Error `json:"error"`
				}
				if json.NewDecoder(response.Body).Decode(&body) == nil && body.Error.Code != "" {
					return &body.Error
				}
				return &core.Error{Code: "unavailable", Message: fmt.Sprintf("query failed: HTTP %d", response.StatusCode), Retryable: true}
			}

			_, err = io.Copy(cmd.OutOrStdout(), response.Body)
			return err
		}}
		switch kind {
		case "topic-activity":
			cmd.Use, cmd.Args = kind, cobra.NoArgs
			cmd.Flags().StringVar(&activityFilter, "filter", "", "Show all, message or issue activity")
		case "router-messages":
			cmd.Use, cmd.Args = kind, cobra.NoArgs
		}
		observe.AddCommand(cmd)
	}
	root.AddCommand(observe)
	root.AddCommand(newRecoverInput(), newInspectBinding(), newReleaseEcho())
	return root
}

func runCore(ctx context.Context, cmd *cobra.Command, config core.Config) (result error) {
	if ctx.Err() != nil {
		return nil
	}
	config.ObserverOTLPEndpoint = os.Getenv("SUPERAGENT_OBSERVER_OTLP_ENDPOINT")
	logger := slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), nil))
	telemetry, err := newTelemetryRuntime(ctx, logger)
	if err != nil {
		return err
	}
	defer telemetry.shutdown()
	server, err := core.StartWithContext(ctx, config, telemetry.options)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = server.Serve()
	}()
	defer func() {
		budget := coreShutdownTimeout
		if config.Massd != nil && config.Massd.Embedded != nil {
			budget += massd.ShutdownTimeout
		}
		shutdown, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			logger.Error("Core shutdown incomplete", "reason", "shutdown_failed")
			result = errors.Join(result, err)
		}
		<-done
	}()

	addresses := map[string]string{"endpoint": server.Endpoint(), "observerEndpoint": server.Endpoint() + "/observer/", "status": "ready"}
	if ctx.Err() != nil {
		return nil
	}
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(addresses); err != nil {
		return err
	}
	select {
	case <-done:
		return serveErr
	case <-ctx.Done():
		return nil
	}
}
