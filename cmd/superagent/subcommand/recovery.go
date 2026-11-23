package subcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/spf13/cobra"

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
	var endpoint, tokenFile string
	cmd := &cobra.Command{
		Use: "recover-input SESSION_BINDING_ID", Short: "Reconcile a local SessionBinding before retry, settlement or replacement",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/core/session-bindings/" + url.PathEscape(args[0]) + "/recoveries"
			return operatorRequest(cmd.Context(), cmd.OutOrStdout(), endpoint, tokenFile, http.MethodPost, path, req)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:24370", "Local Core HTTP endpoint")
	cmd.Flags().StringVar(&tokenFile, "operator-token-file", "", "Private Core operator token file")
	cmd.Flags().StringVar(&req.Mode, "mode", "", "retry (same authority), settle (retired binding), or replace")
	cmd.Flags().BoolVar(&req.StopRuntime, "stop-runtime", false, "Settle the shared local Runtime, interrupting its other Sessions")
	cmd.Flags().StringVar(&req.OperationID, "operation-id", "", "Stable ID for this recovery")
	cmd.Flags().StringVar(&req.Reconciliation, "reconciliation", "", "Facts checked and reason for recovery")
	cmd.Flags().BoolVar(&req.AcceptPossibleDuplicate, "accept-possible-duplicate", false, "Acknowledge that prior input may already have been applied")
	for _, required := range []string{"operator-token-file", "mode", "operation-id", "reconciliation", "accept-possible-duplicate"} {
		_ = cmd.MarkFlagRequired(required)
	}
	return cmd
}

func newInspectBinding() *cobra.Command {
	var endpoint, tokenFile string
	cmd := &cobra.Command{
		Use: "inspect-session-binding SESSION_BINDING_ID", Short: "Read a SessionBinding and its live massd evidence",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return operatorRequest(cmd.Context(), cmd.OutOrStdout(), endpoint, tokenFile, http.MethodGet, "/v1/core/session-bindings/"+url.PathEscape(args[0])+"/runtime-evidence", nil)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:24370", "Local Core HTTP endpoint")
	cmd.Flags().StringVar(&tokenFile, "operator-token-file", "", "Private Core operator token file")
	_ = cmd.MarkFlagRequired("operator-token-file")
	return cmd
}

func operatorRequest(ctx context.Context, out io.Writer, endpoint, tokenFile, method, path string, input any) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return &core.Error{Code: "invalid_argument", Message: "operator endpoint must be a plain loopback HTTP origin"}
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
	request, err := http.NewRequestWithContext(ctx, method, endpoint+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	c := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
