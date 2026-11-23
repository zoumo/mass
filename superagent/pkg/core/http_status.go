package core

import (
	"context"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/zoumo/mass/massd/pkg/resource"
)

const statusTimeout = 2 * time.Second

// ComponentStatus reports a component's current state and the time of its last
// successful observation in this request.
type ComponentStatus struct {
	Status     string     `json:"status" enum:"ready,starting,degraded,unavailable,disabled,not_managed"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
}

// TriggerStatuses is the status contract between Compose and its Core child.
type TriggerStatuses struct {
	GitHub ComponentStatus `json:"github"`
	DWS    ComponentStatus `json:"dws"`
}

type coreComponentStatus struct {
	CoreStatus
	ObservedAt *time.Time `json:"observedAt,omitempty"`
}

type stackStatus struct {
	Status   string              `json:"status" enum:"ready,degraded"`
	Core     coreComponentStatus `json:"core"`
	Massd    ComponentStatus     `json:"massd"`
	Triggers TriggerStatuses     `json:"triggers"`
}

type probeState struct {
	Status string `json:"status" enum:"alive,ready,unavailable"`
}

type probeResponse struct {
	Status int
	Body   probeState
}

func (s *Server) registerStatus(api *httpAPI) {
	huma.Register(api.huma, huma.Operation{
		Method: http.MethodGet, Path: "/livez", OperationID: "coreLiveness", Tags: []string{"Status"},
		Summary: "Check that the Core process can respond",
		Responses: map[string]*huma.Response{
			"403": api.coreError("Core request origin not allowed"),
		},
	}, func(context.Context, *struct{}) (*probeResponse, error) {
		return &probeResponse{Status: http.StatusOK, Body: probeState{Status: "alive"}}, nil
	})
	huma.Register(api.huma, huma.Operation{
		Method: http.MethodGet, Path: "/readyz", OperationID: "coreReadiness", Tags: []string{"Status"},
		Summary: "Check that Core can admit requests",
		Responses: map[string]*huma.Response{
			"403": api.coreError("Core request origin not allowed"),
			"503": jsonResponse("Core admission unavailable", api.schema(reflect.TypeFor[probeState]())),
		},
	}, func(ctx context.Context, _ *struct{}) (*probeResponse, error) {
		ctx, cancel := context.WithTimeout(ctx, statusTimeout)
		defer cancel()
		if !s.admitting.Load() || s.store.db.PingContext(ctx) != nil {
			return &probeResponse{Status: http.StatusServiceUnavailable, Body: probeState{Status: "unavailable"}}, nil
		}
		return &probeResponse{Status: http.StatusOK, Body: probeState{Status: "ready"}}, nil
	})
	huma.Register(api.huma, huma.Operation{
		Method: http.MethodGet, Path: "/status", OperationID: "getStackStatus", Tags: []string{"Status"},
		Summary:     "Read Core, massd and built-in Trigger status",
		Description: "Returns partial status when a component is unavailable. In standalone Core, GitHub and DWS are not managed. Timer state is reported by Core.",
		Responses: map[string]*huma.Response{
			"403": api.coreError("Core request origin not allowed"),
		},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body stackStatus }, error) {
		return &struct{ Body stackStatus }{Body: s.stackStatus(ctx)}, nil
	})
}

func (s *Server) stackStatus(ctx context.Context) stackStatus {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	status := stackStatus{Status: "ready", Triggers: TriggerStatuses{
		GitHub: ComponentStatus{Status: "not_managed"},
		DWS:    ComponentStatus{Status: "not_managed"},
	}}
	var probes sync.WaitGroup
	probes.Go(func() { status.Massd = s.massdStatus(ctx) })
	if s.config.ComposeTriggerStatus != nil {
		probes.Go(func() { status.Triggers = s.config.ComposeTriggerStatus(ctx) })
	}
	health, err := s.store.observerHealth(ctx)
	if err == nil && s.admitting.Load() {
		observedAt := time.Now().UTC()
		status.Core = coreComponentStatus{CoreStatus: health, ObservedAt: &observedAt}
	} else {
		status.Core = coreComponentStatus{CoreStatus: CoreStatus{
			Status: "unavailable", Ingress: "unavailable",
			TopicStates: map[string]int{}, IssueStates: map[string]int{}, EventStates: map[string]int{},
			OutboxStates: map[string]int{}, TimerStates: map[string]int{},
		}}
	}
	probes.Wait()
	for _, state := range []string{status.Core.Status, status.Massd.Status, status.Triggers.GitHub.Status, status.Triggers.DWS.Status} {
		if state != "ready" && state != "disabled" && state != "not_managed" {
			status.Status = "degraded"
			break
		}
	}
	return status
}

func (s *Server) massdStatus(ctx context.Context) ComponentStatus {
	if s.config.Massd == nil {
		return ComponentStatus{Status: "disabled"}
	}
	observedAt := time.Now().UTC()
	if s.embeddedMassd != nil {
		if s.embeddedMassd.Handler().Ready() {
			return ComponentStatus{Status: "ready", ObservedAt: &observedAt}
		}
		return ComponentStatus{Status: "unavailable", ObservedAt: &observedAt}
	}
	client, err := s.massClient()
	if err != nil {
		return ComponentStatus{Status: "unavailable"}
	}
	defer client.Close()
	var runtimes []resource.Runtime
	if err := client.ListRuntimes(ctx, resource.RuntimeQuery{}, &runtimes); err != nil {
		return ComponentStatus{Status: "unavailable"}
	}
	observedAt = time.Now().UTC()
	return ComponentStatus{Status: "ready", ObservedAt: &observedAt}
}
