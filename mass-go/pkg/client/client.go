// Package client carries HTTP v1 Service values without resource or history caches.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

func (c Client) http() *http.Client {
	base := http.Client{}
	if c.HTTP != nil {
		base = *c.HTTP
	}
	base.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &base
}

func SessionPath(ref resource.SessionRef) (string, error) {
	if ref.SessionID != "" && ref.SessionID != "." && ref.SessionID != ".." && ref.Namespace == "" && ref.Name == "" && utf8.ValidString(ref.SessionID) {
		return "/apis/v1/sessions/" + url.PathEscape(ref.SessionID), nil
	}
	if ref.SessionID == "" && validSegment(ref.Namespace) && validSegment(ref.Name) {
		return "/apis/v1/namespaces/" + url.PathEscape(ref.Namespace) + "/sessions/" + url.PathEscape(ref.Name), nil
	}
	return "", invalid()
}

func decodeResponse(response *http.Response, err error, expected int, out any) error {
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var v runtime.OperationError
		if json.NewDecoder(response.Body).Decode(&v) != nil || v.Code == "" {
			return errors.New("unexpected HTTP response")
		}
		return &v
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
	reader    *bufio.Reader
	terminal  error
	mu        sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func (c Client) Observe(ctx context.Context, ref resource.SessionRef) (*Observer, error) {
	if _, e := SessionPath(ref); e != nil {
		return nil, e
	}
	api, e := c.generated("text/event-stream")
	if e != nil {
		return nil, e
	}
	var r *http.Response
	if ref.SessionID != "" {
		r, e = api.ObserveSessionByID(ctx, ref.SessionID, nil)
	} else {
		r, e = api.ObserveSessionByNameOrID(ctx, ref.Namespace, ref.Name, nil)
	}
	if e != nil {
		return nil, e
	}
	if r.StatusCode != http.StatusOK {
		defer r.Body.Close()
		var v runtime.OperationError
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
	o := &Observer{body: r.Body, reader: bufio.NewReader(r.Body)}
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
	for {
		line, err := o.reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return "", nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if kind == "" && len(data) == 0 {
				continue
			}
			return kind, bytes.TrimSuffix(data, []byte("\n")), nil
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			kind = value
		case "data":
			data = append(data, value...)
			data = append(data, '\n')
		}
	}
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
		var v runtime.OperationError
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
