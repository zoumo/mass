// Package store defines persistence responsibilities, independent of execution.
package store

import (
	"errors"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

var (
	ErrConflict = errors.New("resource conflict")
	ErrNotFound = errors.New("resource not found")
	ErrReadOnly = errors.New("store is read-only")
)

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
type HistoryQuery struct {
	Types []string `json:"types,omitzero"`
	After string   `json:"after,omitempty"`
	Limit int      `json:"limit,omitempty"`
}
type HistoryPage struct {
	Events        []events.SessionEvent `json:"events"`
	Next          string                `json:"next,omitempty"`
	SyncedEventID *string               `json:"syncedEventId"`
}
type HistoryStore interface {
	GetSyncProgress(string) (*string, error)
	Append(string, []events.SessionEvent) (*string, error)
	ListHistory(string, HistoryQuery) (HistoryPage, error)
}
