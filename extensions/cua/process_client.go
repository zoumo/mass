package cua

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"

	"connectrpc.com/connect"

	pb "github.com/zoumo/mass/extensions/cua/internal/processpb"
)

const processServicePath = "/cua.env.v1.ProcessService/"

type processClient struct {
	*computer
	start  *connect.Client[pb.StartProcessRequest, pb.StartProcessResponse]
	attach *connect.Client[pb.ConnectProcessRequest, pb.ConnectProcessResponse]
	list   *connect.Client[pb.ListProcessesRequest, pb.ListProcessesResponse]
	input  *connect.Client[pb.SendInputRequest, pb.SendInputResponse]
	signal *connect.Client[pb.SignalProcessRequest, pb.SignalProcessResponse]
	record *connect.Client[pb.GetExecRecordRequest, pb.GetExecRecordResponse]
}

func newProcessHTTPClient(caFile string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		certs, err := os.ReadFile(caFile)
		if err != nil {
			return nil, errors.New("cannot read the Cua CA file")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("cannot load the system CA certificates")
		}
		if !roots.AppendCertsFromPEM(certs) {
			return nil, errors.New("no PEM certificate found in the Cua CA file")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func newProcessClient(client *http.Client, target *computer) *processClient {
	base := target.url + processServicePath
	option := connect.WithGRPCWeb()
	return &processClient{
		computer: target,
		start:    connect.NewClient[pb.StartProcessRequest, pb.StartProcessResponse](client, base+"StartProcess", option),
		attach:   connect.NewClient[pb.ConnectProcessRequest, pb.ConnectProcessResponse](client, base+"ConnectProcess", option),
		list:     connect.NewClient[pb.ListProcessesRequest, pb.ListProcessesResponse](client, base+"ListProcesses", option),
		input:    connect.NewClient[pb.SendInputRequest, pb.SendInputResponse](client, base+"SendInput", option),
		signal:   connect.NewClient[pb.SignalProcessRequest, pb.SignalProcessResponse](client, base+"SignalProcess", option),
		record:   connect.NewClient[pb.GetExecRecordRequest, pb.GetExecRecordResponse](client, base+"GetExecRecord", option),
	}
}

func processRequest[T any](client *processClient, message *T) *connect.Request[T] {
	req := connect.NewRequest(message)
	req.Header().Set("Authorization", "Bearer "+client.token)
	return req
}

func processRPCError(ctx context.Context, method string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if connect.IsWireError(err) {
		// Upstream status text can contain arguments, local paths or credentials.
		return fmt.Errorf("ProcessService %s failed (%s)", method, connect.CodeOf(err))
	}
	return fmt.Errorf("ProcessService %s failed at the relay or transport (%s)", method, connect.CodeOf(err))
}
