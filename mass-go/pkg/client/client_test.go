package client_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type framingSuite struct{ suite.Suite }

func TestFramingSuite(t *testing.T) { suite.Run(t, new(framingSuite)) }
func (s *framingSuite) TestTermination() {
	for _, kind := range []string{"disconnect", "end", "error"} {
		s.Run(kind, func() {
			// Protocol-only frames; these are not fabricated Agent facts.
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: ready\ndata: {}\n\n")
				if kind == "end" {
					fmt.Fprint(w, "event: end\ndata: {}\n\n")
				}
				if kind == "error" {
					fmt.Fprint(w, `event: error
data: {"code":"outcome_unknown","reason":"operation_unconfirmed","message":"Operation failed"}

`)
				}
			}))
			defer h.Close()
			o, e := (client.Client{URL: h.URL}).Observe(context.Background(), resource.SessionRef{SessionID: "synthetic"})
			s.Require().NoError(e)
			_, first := o.Next()
			s.Error(first)
			switch kind {
			case "disconnect":
				s.ErrorIs(first, io.ErrUnexpectedEOF)
				s.False(errors.Is(first, io.EOF))
			case "end":
				s.ErrorIs(first, io.EOF)
			case "error":
				var v *transport.Error
				s.ErrorAs(first, &v)
			}
			s.NoError(o.Close())
			_, again := o.Next()
			s.Equal(first, again, "abnormal result survives close and further next")
		})
	}
}
func (s *framingSuite) TestCloseReleasesBlockedNext() {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "event: ready\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer h.Close()
	o, e := (client.Client{URL: h.URL}).Observe(context.Background(), resource.SessionRef{SessionID: "synthetic"})
	s.Require().NoError(e)
	done := make(chan error, 1)
	go func() { _, e := o.Next(); done <- e }()
	s.NoError(o.Close())
	s.ErrorIs(<-done, io.EOF)
}
