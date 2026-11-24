package core

import (
	"context"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

const watchdogReadPath = coreResourcePrefix + "watchdog"

type WatchdogSnapshot struct {
	Enabled bool           `json:"enabled"`
	Running bool           `json:"running"`
	Latest  *WatchdogCheck `json:"latest,omitempty"`
}

type WatchdogCheck struct {
	ReleaseUnconfirmed bool      `json:"releaseUnconfirmed,omitempty"`
	ObservedAt         time.Time `json:"observedAt"`
	Status             string    `json:"status" enum:"finding,no_finding,incomplete"`
	Reason             string    `json:"reason"`
	TopicID            string    `json:"topicId,omitempty"`
	IssueID            string    `json:"issueId,omitempty"`
	Judgment           string    `json:"judgment,omitempty"`
	Recommendation     string    `json:"recommendation,omitempty"`
}

func (s *Server) registerWatchdogRead(api *httpAPI) {
	registerJSONRead(s, api, huma.Operation{
		Path: watchdogReadPath, OperationID: "getWatchdog", Tags: []string{"Core"},
		Summary:   "Read the latest Compose Watchdog check",
		Responses: api.readResponses(api.schema(reflect.TypeFor[WatchdogSnapshot]()), false),
	}, func(ctx context.Context, _ *struct{}, _ bool) (WatchdogSnapshot, error) {
		if s.config.ComposeWatchdog == nil {
			return WatchdogSnapshot{}, nil
		}
		snapshot, err := s.config.ComposeWatchdog(ctx)
		if err != nil {
			return WatchdogSnapshot{}, problem("unavailable", "Watchdog snapshot unavailable")
		}
		return snapshot, nil
	})
}
