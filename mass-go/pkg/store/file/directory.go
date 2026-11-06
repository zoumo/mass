package file

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func (s *Store) ListRuntimes(q resource.RuntimeQuery) ([]resource.Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(false); err != nil {
		return nil, err
	}
	rows, err := s.sql.conn.QueryContext(context.Background(), `SELECT payload FROM runtime WHERE (?='' OR type=?) AND (?='' OR state=?) ORDER BY name`, q.Type, q.Type, q.State, q.State)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []resource.Runtime{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var r resource.Runtime
		if err := decode(b, &r); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

type directoryCursor struct {
	Kind     string                `json:"kind"`
	Query    resource.SessionQuery `json:"query"`
	Position string                `json:"position"`
}

func sessionOrder(r resource.Session) string {
	return strings.Join([]string{r.Metadata.Namespace, r.Metadata.Name, r.Metadata.UID}, "\x00")
}

func (s *Store) ListSessions(q resource.SessionQuery) (resource.SessionPage, error) {
	if err := resource.ValidateSessionState(q.State); err != nil {
		return resource.SessionPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	empty := resource.SessionPage{}
	if err := s.usable(false); err != nil {
		return empty, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 {
		return empty, runtime.InvalidArgument("limit must be positive")
	}
	after := q.After
	q.After, q.Limit = "", 0
	position := ""
	if after != "" {
		b, err := base64.RawURLEncoding.DecodeString(after)
		if err != nil || len(b) <= 32 {
			return empty, runtime.InvalidArgument("invalid resource cursor")
		}
		mac := hmac.New(sha256.New, []byte(s.data.CursorKey))
		mac.Write(b[32:])
		var c directoryCursor
		if !hmac.Equal(b[:32], mac.Sum(nil)) || decode(b[32:], &c) != nil || c.Kind != "sessions" || !equal(c.Query, q) || c.Position == "" {
			return empty, runtime.InvalidArgument("cursor does not match resource query")
		}
		position = c.Position
	}
	rows, err := s.sql.conn.QueryContext(context.Background(), `SELECT payload FROM session WHERE (?='' OR namespace=?) AND (?='' OR runtime_ref=?) AND (?='' OR state=?) AND (?='' OR session_id=?) AND sort_key>? ORDER BY sort_key LIMIT ?`, q.Namespace, q.Namespace, q.RuntimeRef, q.RuntimeRef, q.State, q.State, q.SessionID, q.SessionID, position, int64(limit)+1)
	if err != nil {
		return empty, err
	}
	defer rows.Close()
	items := []resource.Session{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return empty, err
		}
		var r resource.Session
		if err := decode(b, &r); err != nil {
			return empty, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return empty, err
	}
	page := resource.SessionPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		b, err := json.Marshal(directoryCursor{Kind: "sessions", Query: q, Position: sessionOrder(items[limit-1])})
		if err != nil {
			return empty, err
		}
		mac := hmac.New(sha256.New, []byte(s.data.CursorKey))
		mac.Write(b)
		page.Next = base64.RawURLEncoding.EncodeToString(append(mac.Sum(nil), b...))
	}
	return copyValue(page)
}

func (s *Store) DeleteRuntime(name, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.next()
	if err != nil {
		return err
	}
	r, ok := n.Runtimes[name]
	if !ok {
		return store.ErrNotFound
	}
	if version != r.Metadata.ResourceVersion {
		return store.ErrConflict
	}
	delete(n.Runtimes, name)
	return s.commit(n)
}

func (s *Store) DeleteSession(ref resource.SessionRef, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.next()
	if err != nil {
		return err
	}
	r, err := resolve(n, ref)
	if err != nil {
		return err
	}
	if version != r.Metadata.ResourceVersion {
		return store.ErrConflict
	}
	delete(n.Sessions, key(r.Metadata.Namespace, r.Metadata.Name))
	return s.commit(n)
}
