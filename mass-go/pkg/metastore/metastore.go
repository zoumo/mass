// Package metastore defines independent resource persistence.
package metastore

import "github.com/zoumo/mass/mass-go/pkg/resource"

type MetaStore interface {
	CreateRuntime(resource.Runtime) (resource.Runtime, error)
	GetRuntime(string) (resource.Runtime, error)
	UpdateRuntime(resource.Runtime) (resource.Runtime, error)
	ListRuntimes(resource.RuntimeQuery) ([]resource.Runtime, error)
	DeleteRuntime(string, string) error
	CreateSession(resource.Session) (resource.Session, error)
	GetSession(resource.SessionRef) (resource.Session, error)
	UpdateSession(resource.Session) (resource.Session, error)
	ListSessions(resource.SessionQuery) (resource.SessionPage, error)
	DeleteSession(resource.SessionRef, string) error
}

// Config selects one metadata authority. The zero value selects SQLite.
type Config struct {
	Backend string `mapstructure:"backend"`
}
