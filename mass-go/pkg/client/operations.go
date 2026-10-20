package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/client/generated"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func (c Client) generated(accept string) (*generated.Client, error) {
	return generated.NewClient(c.URL, generated.WithHTTPClient(c.http()), generated.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("Accept", accept)
		if c.Token != "" {
			r.Header.Set("Authorization", "Bearer "+c.Token)
		}
		return nil
	}))
}

// Answer sends one JSON value, including literal null for nil. It never replays.
func (c Client) Answer(ctx context.Context, ref resource.SessionRef, interaction string, answer, out any) error {
	if _, err := SessionPath(ref); err != nil {
		return err
	}
	if interaction == "" || !utf8.ValidString(interaction) {
		return invalid()
	}
	if err := events.CheckDynamicJSON(answer); err != nil {
		return invalid()
	}
	b, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(interaction))
	return c.jsonResponse(http.StatusOK, out, func(api *generated.Client) (*http.Response, error) {
		if ref.SessionID != "" {
			return api.AnswerInteractionByID(ctx, ref.SessionID, encoded, b)
		}
		return api.AnswerInteractionByNameOrID(ctx, ref.Namespace, ref.Name, encoded, b)
	})
}

// jsonResponse keeps all open response values as json.Number, never float64.
func (c Client) jsonResponse(expected int, out any, call func(*generated.Client) (*http.Response, error)) error {
	api, err := c.generated("application/json")
	if err != nil {
		return err
	}
	response, err := call(api)
	return decodeResponse(response, err, expected, out)
}

func generatedValue[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func (c Client) CreateRuntime(ctx context.Context, v resource.Runtime, out any) error {
	if err := events.CheckDynamicJSON(map[string]any(v.Spec.Config)); err != nil {
		return invalid()
	}
	spec, err := generatedValue[generated.RuntimeSpec](v.Spec)
	if err != nil {
		return err
	}
	body := generated.RuntimeCreate{Metadata: generated.CreateMetadata{Name: v.Metadata.Name, Namespace: &v.Metadata.Namespace}, Spec: spec}
	return c.jsonResponse(201, out, func(api *generated.Client) (*http.Response, error) { return api.CreateRuntime(ctx, body) })
}

func (c Client) GetRuntime(ctx context.Context, name string, out any) error {
	if !validSegment(name) {
		return invalid()
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) { return api.GetRuntime(ctx, name) })
}

func (c Client) StartRuntime(ctx context.Context, name string, out any) error {
	if !validSegment(name) {
		return invalid()
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) {
		return api.StartRuntimeWithBody(ctx, name, "application/json", nil)
	})
}

func (c Client) StopRuntime(ctx context.Context, name string, out any) error {
	if !validSegment(name) {
		return invalid()
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) {
		return api.StopRuntimeWithBody(ctx, name, "application/json", nil)
	})
}

func (c Client) DeleteRuntime(ctx context.Context, name string) error {
	if !validSegment(name) {
		return invalid()
	}
	return c.jsonResponse(204, nil, func(api *generated.Client) (*http.Response, error) { return api.DeleteRuntime(ctx, name) })
}

func (c Client) ListRuntimes(ctx context.Context, q resource.RuntimeQuery, out any) error {
	p := generated.ListRuntimesParams{Type: optional(q.Type), State: optional(generated.ListRuntimesParamsState(q.State))}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) { return api.ListRuntimes(ctx, &p) })
}

func (c Client) UpdateRuntime(ctx context.Context, name string, spec resource.RuntimeSpec, out any) error {
	if err := events.CheckDynamicJSON(map[string]any(spec.Config)); err != nil {
		return invalid()
	}
	if !validSegment(name) {
		return invalid()
	}
	body, err := generatedValue[generated.RuntimeSpec](spec)
	if err != nil {
		return err
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) { return api.UpdateRuntime(ctx, name, body) })
}

func (c Client) CreateSession(ctx context.Context, v resource.Session, out any) error {
	spec, err := generatedValue[generated.SessionSpec](v.Spec)
	if err != nil {
		return err
	}
	body := generated.SessionCreate{Metadata: generated.SessionCreateMetadata{Name: v.Metadata.Name, Namespace: v.Metadata.Namespace}, Spec: spec}
	return c.jsonResponse(201, out, func(api *generated.Client) (*http.Response, error) { return api.CreateSession(ctx, body) })
}

func (c Client) ListSessions(ctx context.Context, q resource.SessionQuery, out any) error {
	if q.Limit < 0 || q.SessionID != "" {
		return invalid()
	}
	p := generated.ListSessionsParams{Namespace: optional(q.Namespace), RuntimeRef: optional(q.RuntimeRef), State: optional(generated.ListSessionsParamsState(q.State)), After: optional(q.After), Limit: optional(int64(q.Limit))}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) { return api.ListSessions(ctx, &p) })
}

func optional[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}
func validSegment(v string) bool { return v != "" && v != "." && v != ".." && utf8.ValidString(v) }

func (c Client) GetSession(ctx context.Context, ref resource.SessionRef, out any) error {
	if _, err := SessionPath(ref); err != nil {
		return err
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) {
		if ref.SessionID != "" {
			return api.GetSessionByID(ctx, ref.SessionID)
		}
		return api.GetSessionByNameOrID(ctx, ref.Namespace, ref.Name)
	})
}

func (c Client) ArchiveSession(ctx context.Context, ref resource.SessionRef, out any) error {
	if _, err := SessionPath(ref); err != nil {
		return err
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) {
		if ref.SessionID != "" {
			return api.ArchiveSessionByIDWithBody(ctx, ref.SessionID, "application/json", nil)
		}
		return api.ArchiveSessionByNameOrIDWithBody(ctx, ref.Namespace, ref.Name, "application/json", nil)
	})
}

func (c Client) ResumeSession(ctx context.Context, ref resource.SessionRef, out any) error {
	if _, err := SessionPath(ref); err != nil {
		return err
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) {
		if ref.SessionID != "" {
			return api.ResumeSessionByIDWithBody(ctx, ref.SessionID, "application/json", nil)
		}
		return api.ResumeSessionByNameOrIDWithBody(ctx, ref.Namespace, ref.Name, "application/json", nil)
	})
}

func (c Client) Cancel(ctx context.Context, ref resource.SessionRef, out any) error {
	if _, err := SessionPath(ref); err != nil {
		return err
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) {
		if ref.SessionID != "" {
			return api.CancelByIDWithBody(ctx, ref.SessionID, "application/json", nil)
		}
		return api.CancelByNameOrIDWithBody(ctx, ref.Namespace, ref.Name, "application/json", nil)
	})
}

func (c Client) SendPrompt(ctx context.Context, ref resource.SessionRef, input runtime.Input, out any) error {
	for _, block := range input.Content {
		if err := block.Validate(); err != nil {
			return invalid()
		}
	}
	if _, err := SessionPath(ref); err != nil {
		return err
	}
	body, err := generatedValue[generated.Input](input)
	if err != nil {
		return err
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) {
		if ref.SessionID != "" {
			return api.SendPromptByID(ctx, ref.SessionID, body)
		}
		return api.SendPromptByNameOrID(ctx, ref.Namespace, ref.Name, body)
	})
}

func (c Client) History(ctx context.Context, ref resource.SessionRef, q store.HistoryQuery, out any) error {
	if _, err := SessionPath(ref); err != nil {
		return err
	}
	if q.Limit < 0 || (q.Types != nil && len(q.Types) == 0) {
		return invalid()
	}
	return c.jsonResponse(200, out, func(api *generated.Client) (*http.Response, error) {
		if ref.SessionID != "" {
			p := generated.ListHistoryByIDParams{After: optional(q.After), Limit: optional(int64(q.Limit))}
			if q.Types != nil {
				types := make([]generated.ListHistoryByIDParamsTypes, len(q.Types))
				for i, v := range q.Types {
					types[i] = generated.ListHistoryByIDParamsTypes(v)
				}
				p.Types = &types
			}
			return api.ListHistoryByID(ctx, ref.SessionID, &p)
		}
		p := generated.ListHistoryByNameOrIDParams{After: optional(q.After), Limit: optional(int64(q.Limit))}
		if q.Types != nil {
			types := make([]generated.ListHistoryByNameOrIDParamsTypes, len(q.Types))
			for i, v := range q.Types {
				types[i] = generated.ListHistoryByNameOrIDParamsTypes(v)
			}
			p.Types = &types
		}
		return api.ListHistoryByNameOrID(ctx, ref.Namespace, ref.Name, &p)
	})
}

func invalid() *runtime.OperationError {
	return &runtime.OperationError{Code: "invalid_argument", Reason: "invalid_request", Message: "Invalid request"}
}
