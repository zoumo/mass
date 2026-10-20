// Package transport maps the HTTP v1 contract to application services.
package transport

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/service"
)

type API interface {
	service.RuntimeService
	service.SessionService
}

// Options selects authenticated instance access or explicit trusted local access.
type Options struct {
	Token     string
	LocalOnly bool
}
type Server struct {
	api     API
	options Options
	ready   atomic.Bool
	life    context.Context
	stop    context.CancelFunc
	handler http.Handler
}

func New(api API, opts Options) (*Server, error) {
	if (opts.LocalOnly && opts.Token != "") || (!opts.LocalOnly && opts.Token == "") || strings.ContainsAny(opts.Token, " \t\r\n") {
		return nil, Invalid()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{api: api, options: opts, life: ctx, stop: cancel}
	s.ready.Store(true)
	router, _ := s.routes()
	s.handler = router
	return s, nil
}

// Shutdown closes admission and observation. Service retains operation ownership.
func (s *Server) Shutdown()                                        { s.ready.Store(false); s.stop() }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) authorized(r *http.Request) bool {
	if s.options.LocalOnly {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		return err == nil && net.ParseIP(host).IsLoopback()
	}
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return false
	}
	parts := strings.Fields(headers[0])
	return len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.options.Token)) == 1
}

func hasBody(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody && (r.ContentLength != 0 || len(r.TransferEncoding) > 0)
}

func accepts(header, media string) bool {
	if header == "" {
		return true
	}
	best := -1
	quality := 0.0
	for part := range strings.SplitSeq(header, ",") {
		kind, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if raw, ok := params["q"]; ok {
			q, err = strconv.ParseFloat(raw, 64)
			if err != nil || q < 0 || q > 1 {
				continue
			}
		}
		specificity := -1
		switch {
		case kind == media:
			specificity = 2
		case kind == strings.Split(media, "/")[0]+"/*":
			specificity = 1
		case kind == "*/*":
			specificity = 0
		}
		if specificity > best {
			best = specificity
			quality = q
		}
	}
	return best >= 0 && quality > 0
}

func decodeRef(v string) (string, error) {
	b, e := base64.RawURLEncoding.Strict().DecodeString(v)
	if e != nil || len(b) == 0 || !utf8.Valid(b) || base64.RawURLEncoding.EncodeToString(b) != v {
		return "", Invalid()
	}
	return string(b), nil
}
