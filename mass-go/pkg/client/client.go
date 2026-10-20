// Package client carries HTTP v1 Service values without resource or history caches.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// Params is a CLI/client convenience value, never an HTTP wire envelope.
type Params struct {
	Name         string                `json:"name,omitempty"`
	Session      resource.SessionRef   `json:"session,omitzero"`
	Runtime      *resource.Runtime     `json:"runtime,omitempty"`
	Create       *resource.Session     `json:"create,omitempty"`
	Spec         *resource.RuntimeSpec `json:"spec,omitempty"`
	RuntimeQuery resource.RuntimeQuery `json:"runtimeQuery,omitzero"`
	SessionQuery resource.SessionQuery `json:"sessionQuery,omitzero"`
	History      store.HistoryQuery    `json:"history,omitzero"`
	Input        runtime.Input         `json:"input,omitzero"`
	Interaction  string                `json:"interaction,omitempty"`
	Answer       any                   `json:"answer,omitempty"`
}

// Typed creation calls project only the writable fields. CallJSON leaves supplied JSON intact.
func (p Params) MarshalJSON() ([]byte, error) {
	type fields Params
	b, err := json.Marshal(fields(p))
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for _, key := range []string{"runtime", "create"} {
		if raw, ok := out[key]; ok {
			var v struct {
				Metadata struct {
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
				} `json:"metadata"`
				Spec json.RawMessage `json:"spec"`
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			out[key], err = json.Marshal(v)
			if err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(out)
}

func (c Client) http() *http.Client {
	base := http.Client{}
	if c.HTTP != nil {
		base = *c.HTTP
	}
	base.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &base
}

func (c Client) request(ctx context.Context, method, path string, body json.RawMessage, accept string) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Accept", accept)
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.http().Do(r)
}

func SessionPath(ref resource.SessionRef) (string, error) {
	if ref.SessionID != "" && ref.Namespace == "" && ref.Name == "" && utf8.ValidString(ref.SessionID) {
		return "/v1/sessions/" + base64.RawURLEncoding.EncodeToString([]byte(ref.SessionID)), nil
	}
	if ref.SessionID == "" && ref.Namespace != "" && ref.Name != "" {
		return "/v1/namespaces/" + url.PathEscape(ref.Namespace) + "/sessions/" + url.PathEscape(ref.Name), nil
	}
	return "", transport.Invalid()
}

func EncodeParams(op string, p Params) (json.RawMessage, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	allowed, ok := operationFields[op]
	if !ok {
		return nil, transport.Invalid()
	}
	for key := range fields {
		if !allowed[key] {
			delete(fields, key)
		}
	}
	return json.Marshal(fields)
}

func (c Client) Call(ctx context.Context, op string, p Params, out any) error {
	b, err := EncodeParams(op, p)
	if err != nil {
		return err
	}
	return c.CallJSON(ctx, op, b, out)
}

var operationFields = func() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for op, fields := range map[string]string{"runtime.create": "runtime", "runtime.list": "runtimeQuery", "runtime.get": "name", "runtime.update": "name spec", "runtime.start": "name", "runtime.stop": "name", "runtime.delete": "name", "session.create": "create", "session.list": "sessionQuery", "session.get": "session", "session.archive": "session", "session.resume": "session", "session.cancel": "session", "session.input": "session input", "session.answer": "session interaction answer", "session.history": "session history"} {
		out[op] = map[string]bool{}
		for key := range strings.FieldsSeq(fields) {
			out[op][key] = true
		}
	}
	return out
}()

func (c Client) CallJSON(ctx context.Context, op string, p json.RawMessage, out any) error {
	if transport.ValidateJSON(p) != nil {
		return transport.Invalid()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(p, &fields) != nil || fields == nil {
		return transport.Invalid()
	}
	allowed, ok := operationFields[op]
	if !ok {
		return transport.Invalid()
	}
	for key := range fields {
		if !allowed[key] {
			return transport.Invalid()
		}
	}
	decode := func(key string, v any) error {
		raw, ok := fields[key]
		if !ok || string(raw) == "null" {
			return transport.Invalid()
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		d.DisallowUnknownFields()
		if d.Decode(v) != nil {
			return transport.Invalid()
		}
		return nil
	}
	method, path := "POST", ""
	var body json.RawMessage
	query := url.Values{}
	if strings.HasPrefix(op, "runtime.") && op != "runtime.create" && op != "runtime.list" {
		var name string
		if decode("name", &name) != nil || name == "" {
			return transport.Invalid()
		}
		path = "/v1/runtimes/" + url.PathEscape(name)
	}
	if strings.HasPrefix(op, "session.") && op != "session.create" && op != "session.list" {
		var ref resource.SessionRef
		if decode("session", &ref) != nil {
			return transport.Invalid()
		}
		var err error
		path, err = SessionPath(ref)
		if err != nil {
			return err
		}
	}
	switch op {
	case "runtime.create":
		path = "/v1/runtimes"
		body = fields["runtime"]
	case "runtime.list":
		path = "/v1/runtimes"
		method = "GET"
		if _, ok := fields["runtimeQuery"]; ok {
			var q resource.RuntimeQuery
			if decode("runtimeQuery", &q) != nil {
				return transport.Invalid()
			}
			if q.Type != "" {
				query.Set("type", q.Type)
			}
			if q.State != "" {
				query.Set("state", q.State)
			}
		}
	case "runtime.get", "session.get":
		method = "GET"
	case "runtime.update":
		method = "PUT"
		path += "/spec"
		body = fields["spec"]
	case "runtime.start":
		path += "/start"
	case "runtime.stop":
		path += "/stop"
	case "runtime.delete":
		method = "DELETE"
	case "session.create":
		path = "/v1/sessions"
		body = fields["create"]
	case "session.list":
		path = "/v1/sessions"
		method = "GET"
		if _, ok := fields["sessionQuery"]; ok {
			var q struct {
				Namespace  string `json:"namespace"`
				RuntimeRef string `json:"runtimeRef"`
				State      string `json:"state"`
				After      string `json:"after"`
				Limit      *int   `json:"limit"`
			}
			if decode("sessionQuery", &q) != nil {
				return transport.Invalid()
			}
			for k, v := range map[string]string{"namespace": q.Namespace, "runtimeRef": q.RuntimeRef, "state": q.State, "after": q.After} {
				if v != "" {
					query.Set(k, v)
				}
			}
			if q.Limit != nil {
				query.Set("limit", strconv.Itoa(*q.Limit))
			}
		}
	case "session.history":
		method = "GET"
		path += "/events"
		if _, ok := fields["history"]; ok {
			var q store.HistoryQuery
			if decode("history", &q) != nil {
				return transport.Invalid()
			}
			if q.Types != nil {
				if len(q.Types) == 0 {
					return transport.Invalid()
				}
				query["types"] = q.Types
			}
			if q.Limit != 0 {
				query.Set("limit", strconv.Itoa(q.Limit))
			}
			if q.After != "" {
				query.Set("after", q.After)
			}
		}
	case "session.input":
		path += "/inputs"
		body = fields["input"]
	case "session.answer":
		var interaction string
		if decode("interaction", &interaction) != nil || interaction == "" || !utf8.ValidString(interaction) {
			return transport.Invalid()
		}
		path += "/interactions/" + base64.RawURLEncoding.EncodeToString([]byte(interaction)) + "/answer"
		var ok bool
		body, ok = fields["answer"]
		if !ok {
			return transport.Invalid()
		}
	case "session.cancel":
		path += "/cancel"
	case "session.resume":
		path += "/resume"
	case "session.archive":
		path += "/archive"
	}
	if (op == "runtime.create" || op == "runtime.update" || op == "session.create" || op == "session.input") && len(body) == 0 {
		return transport.Invalid()
	}
	for _, key := range []string{"sessionQuery", "history"} {
		if raw, ok := fields[key]; ok {
			var q map[string]json.RawMessage
			_ = json.Unmarshal(raw, &q)
			if limit, ok := q["limit"]; ok {
				var n int
				if json.Unmarshal(limit, &n) != nil || n < 1 {
					return transport.Invalid()
				}
				query.Set("limit", strconv.Itoa(n))
			}
		}
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	response, err := c.request(ctx, method, path, body, "application/json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var v transport.Error
		if json.NewDecoder(response.Body).Decode(&v) != nil || v.Code == "" {
			return errors.New("unexpected HTTP response")
		}
		return &v
	}
	expected := 200
	if op == "runtime.create" || op == "session.create" {
		expected = 201
	}
	if op == "runtime.delete" {
		expected = 204
	}
	if response.StatusCode != expected {
		return errors.New("unexpected HTTP status")
	}
	if expected == 204 {
		return nil
	}
	if out == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	d := json.NewDecoder(response.Body)
	d.UseNumber()
	return d.Decode(out)
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
	path, e := SessionPath(ref)
	if e != nil {
		return nil, e
	}
	r, e := c.request(ctx, "GET", path+"/events/stream", nil, "text/event-stream")
	if e != nil {
		return nil, e
	}
	if r.StatusCode != http.StatusOK {
		defer r.Body.Close()
		var v transport.Error
		if json.NewDecoder(r.Body).Decode(&v) == nil && v.Code != "" {
			return nil, &v
		}
		return nil, errors.New("observe rejected")
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		r.Body.Close()
		return nil, errors.New("unexpected observation media type")
	}
	scan := bufio.NewScanner(r.Body)
	scan.Buffer(make([]byte, 4096), 16<<20)
	o := &Observer{body: r.Body, scan: scan}
	kind, _, e := o.frame()
	if e != nil || kind != "subscription.ready" {
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
			if kind == "" && len(data) == 0 {
				continue
			}
			return kind, data, nil
		}
		if after, ok := strings.CutPrefix(line, "event: "); ok {
			kind = after
		}
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			data = append(data, after...)
		}
	}
	if e := o.scan.Err(); e != nil {
		return "", nil, e
	}
	return "", nil, io.ErrUnexpectedEOF
}

func (o *Observer) Next() (events.SessionEvent, error) {
	o.mu.Lock()
	terminal := o.terminal
	o.mu.Unlock()
	if terminal != nil {
		return events.SessionEvent{}, terminal
	}
	k, b, e := o.frame()
	if e != nil {
		return events.SessionEvent{}, o.finish(e)
	}
	switch k {
	case "subscription.closed":
		return events.SessionEvent{}, o.finish(io.EOF)
	case "subscription.error":
		var v transport.Error
		if json.Unmarshal(b, &v) != nil {
			return events.SessionEvent{}, o.finish(errors.New("invalid stream error"))
		}
		return events.SessionEvent{}, o.finish(&v)
	case "session.event":
		var v events.SessionEvent
		e = json.Unmarshal(b, &v)
		if e != nil {
			return v, o.finish(e)
		}
		return v, nil
	}
	return events.SessionEvent{}, o.finish(errors.New("invalid stream frame"))
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
	_ = o.finish(io.EOF)
	o.closeOnce.Do(func() { o.closeErr = o.body.Close() })
	return o.closeErr
}
