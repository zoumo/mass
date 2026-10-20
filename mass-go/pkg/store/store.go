// Package store defines shared persistence errors and history query values.
package store

import (
	"errors"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

var (
	ErrConflict = errors.New("resource conflict")
	ErrNotFound = errors.New("resource not found")
	ErrReadOnly = errors.New("store is read-only")
)

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
