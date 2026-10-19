// Package client carries Service values without a local resource or history cache.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"io"
	"net/http"
	"strings"
	"sync"
)

type Client struct {
	URL  string
	HTTP *http.Client
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (c Client) request(ctx context.Context, path string, v any) (*http.Response, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	r, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.URL, "/")+path, bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	r.Header.Set("Content-Type", "application/json")
	return c.http().Do(r)
}
func (c Client) Call(ctx context.Context, op string, p transport.Params, out any) error {
	return c.call(ctx, op, p, out)
}

// CallJSON preserves explicit empty, null and zero values for server validation.
func (c Client) CallJSON(ctx context.Context, op string, p json.RawMessage, out any) error {
	return c.call(ctx, op, p, out)
}
func (c Client) call(ctx context.Context, op string, p any, out any) error {
	r, e := c.request(ctx, "/experimental/operations/"+op, p)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	var v transport.Response
	if e = json.NewDecoder(r.Body).Decode(&v); e != nil {
		return e
	}
	if v.Error != nil {
		return v.Error
	}
	if r.StatusCode != 200 {
		return errors.New("unexpected HTTP status")
	}
	if out != nil {
		return json.Unmarshal(v.Result, out)
	}
	return nil
}

type Observer struct {
	body      io.ReadCloser
	scan      *bufio.Scanner
	terminal  error
	mu        sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func (c Client) Observe(ctx context.Context, ref resource.SessionRef) (*Observer, error) {
	r, e := c.request(ctx, "/experimental/observe", ref)
	if e != nil {
		return nil, e
	}
	if r.StatusCode != 200 {
		defer r.Body.Close()
		var v transport.Response
		if json.NewDecoder(r.Body).Decode(&v) == nil && v.Error != nil {
			return nil, v.Error
		}
		return nil, errors.New("observe rejected")
	}
	scan := bufio.NewScanner(r.Body)
	scan.Buffer(make([]byte, 4096), 16<<20)
	o := &Observer{body: r.Body, scan: scan}
	kind, _, e := o.frame()
	if e != nil || kind != "ready" {
		r.Body.Close()
		return nil, errors.New("observation not established")
	}
	return o, nil
}
func (o *Observer) frame() (string, []byte, error) {
	var kind string
	var data []byte
	for o.scan.Scan() {
		line := o.scan.Text()
		if line == "" {
			return kind, data, nil
		}
		if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = append(data, strings.TrimPrefix(line, "data: ")...)
		}
	}
	if e := o.scan.Err(); e != nil {
		return "", nil, e
	}
	return "", nil, io.ErrUnexpectedEOF
}
func (o *Observer) Next() (runtime.SessionEvent, error) {
	o.mu.Lock()
	terminal := o.terminal
	o.mu.Unlock()
	if terminal != nil {
		return runtime.SessionEvent{}, terminal
	}
	k, b, e := o.frame()
	if e != nil {
		return runtime.SessionEvent{}, o.finish(e)
	}
	switch k {
	case "end":
		return runtime.SessionEvent{}, o.finish(io.EOF)
	case "error":
		var v transport.Error
		if json.Unmarshal(b, &v) != nil {
			return runtime.SessionEvent{}, o.finish(errors.New("invalid stream error"))
		}
		return runtime.SessionEvent{}, o.finish(&v)
	case "session":
		var v runtime.SessionEvent
		e = json.Unmarshal(b, &v)
		if e != nil {
			return v, o.finish(e)
		}
		return v, nil
	}
	return runtime.SessionEvent{}, o.finish(errors.New("invalid stream frame"))
}
func (o *Observer) finish(err error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.terminal == nil {
		o.terminal = err
	}
	return o.terminal
}
func (o *Observer) Close() error {
	o.finish(io.EOF)
	o.closeOnce.Do(func() { o.closeErr = o.body.Close() })
	return o.closeErr
}
