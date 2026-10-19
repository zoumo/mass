// Package commands provides the CLI's stateless HTTP client assembly.
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"io"
)

func Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	f := flag.NewFlagSet("massctl", flag.ContinueOnError)
	f.SetOutput(stderr)
	url := f.String("url", "http://127.0.0.1:8787", "experimental daemon URL")
	op := f.String("op", "", "Service operation, e.g. session.create; observe streams future events")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 || *op == "" {
		return transport.Invalid()
	}
	c := client.Client{URL: *url}
	enc := json.NewEncoder(out)
	d := json.NewDecoder(in)
	d.UseNumber()
	d.DisallowUnknownFields()
	if *op == "observe" {
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
		if e = enc.Encode(map[string]bool{"ready": true}); e != nil {
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
			if e = enc.Encode(v); e != nil {
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
	if e := c.CallJSON(ctx, *op, p, &result); e != nil {
		return e
	}
	return enc.Encode(result)
}
