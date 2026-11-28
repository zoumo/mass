package subcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/zoumo/mass/internal/httpaddress"
	"github.com/zoumo/mass/internal/superagentapi"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func newRecoverInput() *cobra.Command {
	var req struct {
		OperationID             string `json:"operationId"`
		Mode                    string `json:"mode"`
		Reconciliation          string `json:"reconciliation"`
		AcceptPossibleDuplicate bool   `json:"acceptPossibleDuplicate"`
		StopRuntime             bool   `json:"stopRuntime,omitempty"`
	}
	var operator operatorTarget
	cmd := &cobra.Command{
		Use: "recover-input SESSION_BINDING_ID", Short: "Reconcile a local SessionBinding before retry, settlement or replacement",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := superagentapi.Prefix + "/session-bindings/" + url.PathEscape(args[0]) + "/recoveries"
			return operator.request(cmd, http.MethodPost, path, req)
		},
	}
	operator.addFlags(cmd.Flags())
	cmd.Flags().StringVar(&req.Mode, "mode", "", "retry (same authority), settle (retired binding), or replace")
	cmd.Flags().BoolVar(&req.StopRuntime, "stop-runtime", false, "Settle the shared local Runtime, interrupting its other Sessions")
	cmd.Flags().StringVar(&req.OperationID, "operation-id", "", "Stable ID for this recovery")
	cmd.Flags().StringVar(&req.Reconciliation, "reconciliation", "", "Facts checked and reason for recovery")
	cmd.Flags().BoolVar(&req.AcceptPossibleDuplicate, "accept-possible-duplicate", false, "Acknowledge that prior input may already have been applied")
	for _, required := range []string{"mode", "operation-id", "reconciliation", "accept-possible-duplicate"} {
		_ = cmd.MarkFlagRequired(required)
	}
	return cmd
}

func newInspectBinding() *cobra.Command {
	var operator operatorTarget
	cmd := &cobra.Command{
		Use: "inspect-session-binding SESSION_BINDING_ID", Short: "Read a SessionBinding and its live massd evidence",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return operator.request(cmd, http.MethodGet, superagentapi.Prefix+"/session-bindings/"+url.PathEscape(args[0])+"/runtime-evidence", nil)
		},
	}
	operator.addFlags(cmd.Flags())
	return cmd
}

type operatorTarget struct {
	endpoint  string
	tokenFile string
	config    string
}

func (target *operatorTarget) addFlags(flags *pflag.FlagSet) {
	flags.StringVar(&target.endpoint, "endpoint", "http://127.0.0.1:24370", "Core HTTP endpoint")
	flags.StringVar(&target.tokenFile, "operator-token-file", "", "Private Core admin token file")
	flags.StringVar(&target.config, "config", "", "Local Core or Compose YAML configuration file")
}

func (target *operatorTarget) request(cmd *cobra.Command, method, path string, input any) error {
	t := *target
	if t.config != "" {
		config, err := loadOperatorCoreConfig(t.config)
		if err != nil {
			return err
		}
		if t.tokenFile == "" {
			t.tokenFile = config.AdminTokenPath()
		}
		if !cmd.Flags().Changed("endpoint") {
			host, port, _ := httpaddress.SplitListen(config.Listen)
			if host == "" {
				host = "::"
			}
			origin := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(int(port)))}
			endpoint := childHTTPOrigin(origin.String(), host)
			if endpoint == nil || port == 0 {
				return &core.Error{Code: "invalid_argument", Message: "set --endpoint when Core uses a dynamic listen port"}
			}
			t.endpoint = endpoint.String()
		}
	}
	if t.tokenFile == "" {
		return &core.Error{Code: "invalid_argument", Message: "set --config or --operator-token-file"}
	}
	return operatorRequest(cmd.Context(), cmd.OutOrStdout(), t.endpoint, t.tokenFile, method, path, input)
}

func loadOperatorCoreConfig(path string) (core.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return core.Config{}, &core.Error{Code: "invalid_argument", Message: "cannot read YAML configuration"}
	}
	var fields map[string]any
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return core.Config{}, &core.Error{Code: "invalid_argument", Message: "cannot read YAML configuration"}
	}
	if _, composed := fields["core"]; composed {
		config, err := decodeComposeConfig(bytes.NewReader(data))
		if err != nil {
			return core.Config{}, err
		}
		return core.LoadComposeConfig(path, bytes.NewReader(config.core))
	}
	return core.LoadConfig(path)
}

func operatorRequest(ctx context.Context, out io.Writer, endpoint, tokenFile, method, path string, input any) error {
	u, err := httpaddress.ParseOrigin(endpoint)
	if err != nil {
		return &core.Error{Code: "invalid_argument", Message: "operator endpoint " + err.Error()}
	}
	token, err := core.ReadOperatorToken(tokenFile)
	if err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String()+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	c := &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := c.Do(request)
	if err != nil {
		return &core.Error{Code: "unavailable", Message: "operator response unavailable", Retryable: true}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		var p struct {
			Error *core.Error `json:"error"`
		}
		if json.NewDecoder(response.Body).Decode(&p) == nil && p.Error != nil {
			return p.Error
		}
		return &core.Error{Code: "unavailable", Message: "operator response unavailable; verify current state before retrying"}
	}
	_, err = io.Copy(out, response.Body)
	return err
}
