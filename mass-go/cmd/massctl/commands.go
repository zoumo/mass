package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func runCommand(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	var url, op string
	cmd := &cobra.Command{
		Use:           "massctl",
		Short:         "Call the MASS HTTP v1 service",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if op == "" {
				return transport.Invalid()
			}
			return call(cmd.Context(), url, op, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.SetArgs(args)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&url, "url", "http://127.0.0.1:8787", "HTTP v1 daemon URL")
	cmd.Flags().StringVar(&op, "op", "", "Service operation, e.g. session.create; observe streams future events")
	return cmd.ExecuteContext(ctx)
}

func call(ctx context.Context, url, op string, in io.Reader, out io.Writer) error {
	c := client.Client{URL: url, Token: os.Getenv("MASS_TOKEN")}
	enc := json.NewEncoder(out)
	d := json.NewDecoder(in)
	d.UseNumber()
	d.DisallowUnknownFields()
	if op == "observe" {
		var ref resource.SessionRef
		if d.Decode(&ref) != nil {
			return transport.Invalid()
		}
		if d.Decode(new(any)) != io.EOF {
			return transport.Invalid()
		}
		o, e := c.Observe(ctx, ref)
		if e != nil {
			return e
		}
		defer o.Close()
		// Ready is emitted only after the server registered the independent subscription.
		if e := enc.Encode(map[string]bool{"ready": true}); e != nil {
			return e
		}
		for {
			v, e := o.Next()
			if errors.Is(e, io.EOF) {
				return nil
			}
			if e != nil {
				return e
			}
			if e := enc.Encode(v); e != nil {
				return e
			}
		}
	}
	var p json.RawMessage
	if d.Decode(&p) != nil {
		return transport.Invalid()
	}
	if d.Decode(new(any)) != io.EOF {
		return transport.Invalid()
	}
	var result json.RawMessage
	if e := c.CallJSON(ctx, op, p, &result); e != nil {
		return e
	}
	return enc.Encode(result)
}
