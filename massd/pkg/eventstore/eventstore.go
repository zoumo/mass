// Package eventstore defines independent file-authoritative event persistence.
package eventstore

import (
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/store"
)

type EventStore interface {
	GetSyncProgress(string) (*string, error)
	Append(string, []events.SessionEvent) (*string, error)
	ListHistory(string, store.HistoryQuery) (store.HistoryPage, error)
}
