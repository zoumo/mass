// Package resource contains shared persistent values, not Service operations.
package resource

import (
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"time"
)

type Metadata struct {
	Namespace       string    `json:"namespace"`
	Name            string    `json:"name"`
	UID             string    `json:"uid"`
	CreatedAt       time.Time `json:"createdAt"`
	Generation      uint64    `json:"generation"`
	ResourceVersion string    `json:"resourceVersion"`
}
type RuntimeSpec struct {
	Type   string         `json:"type"`
	Config runtime.Config `json:"config"`
}
type RuntimeStatus struct {
	State             string               `json:"state"`
	AppliedGeneration uint64               `json:"appliedGeneration,omitempty"`
	Capabilities      runtime.Capabilities `json:"capabilities,omitempty"`
}
type Runtime struct {
	Metadata Metadata      `json:"metadata"`
	Spec     RuntimeSpec   `json:"spec"`
	Status   RuntimeStatus `json:"status"`
}
type SessionSpec struct {
	runtime.SessionSpec
	RuntimeRef string `json:"runtimeRef"`
}
type SessionStatus struct {
	SessionID         string               `json:"sessionId"`
	State             runtime.SessionState `json:"state"`
	AppliedGeneration uint64               `json:"appliedGeneration,omitempty"`
	RuntimeGeneration uint64               `json:"runtimeGeneration,omitempty"`
	Capabilities      runtime.Capabilities `json:"capabilities,omitempty"`
}
type Session struct {
	Metadata Metadata      `json:"metadata"`
	Spec     SessionSpec   `json:"spec"`
	Status   SessionStatus `json:"status"`
}
type SessionRef struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type RuntimeQuery struct {
	Type  string `json:"type,omitempty"`
	State string `json:"state,omitempty"`
}
type SessionQuery struct {
	Namespace  string               `json:"namespace,omitempty"`
	RuntimeRef string               `json:"runtimeRef,omitempty"`
	State      runtime.SessionState `json:"state,omitempty"`
	SessionID  string               `json:"sessionId,omitempty"`
	After      string               `json:"after,omitempty"`
	Limit      int                  `json:"limit,omitempty"`
}
type SessionPage struct {
	Items []Session `json:"items"`
	Next  string    `json:"next,omitempty"`
}
