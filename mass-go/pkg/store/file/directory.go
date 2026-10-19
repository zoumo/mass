package file

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
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
	items := []resource.Runtime{}
	for _, r := range s.data.Runtimes {
		if (q.Type == "" || r.Spec.Type == q.Type) && (q.State == "" || r.Status.State == q.State) {
			items = append(items, r)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Metadata.Name < items[j].Metadata.Name })
	return copyValue(items)
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
	items := []resource.Session{}
	for _, r := range s.data.Sessions {
		if (q.Namespace == "" || r.Metadata.Namespace == q.Namespace) && (q.RuntimeRef == "" || r.Spec.RuntimeRef == q.RuntimeRef) && (q.State == "" || r.Status.State == q.State) && (q.SessionID == "" || r.Status.SessionID == q.SessionID) && sessionOrder(r) > position {
			items = append(items, r)
		}
	}
	sort.Slice(items, func(i, j int) bool { return sessionOrder(items[i]) < sessionOrder(items[j]) })
	page := resource.SessionPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		b, _ := json.Marshal(directoryCursor{Kind: "sessions", Query: q, Position: sessionOrder(items[limit-1])})
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
	delete(n.History, r.Status.SessionID)
	return s.commit(n)
}
