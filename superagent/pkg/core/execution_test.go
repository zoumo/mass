package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionReuseRequiresMatchingProfileAndLocation(t *testing.T) {
	previous := Binding{
		ID: "previous", SessionID: "native", SessionUID: "uid", Name: "old", Workspace: "/project", Namespace: "topic",
		ExecutionProfile: ExecutionProfile{RuntimeRef: "agent", Model: "model", Effort: "medium"},
		Close:            &BindingClose{Reusable: true},
	}
	for _, tc := range []struct {
		name   string
		modify func(*Binding)
		reuse  bool
	}{
		{name: "all equal", reuse: true},
		{name: "runtime", modify: func(b *Binding) { b.RuntimeRef = "other" }},
		{name: "model", modify: func(b *Binding) { b.Model = "other" }},
		{name: "effort", modify: func(b *Binding) { b.Effort = "" }},
		{name: "workspace", modify: func(b *Binding) { b.Workspace = "/other" }},
		{name: "namespace", modify: func(b *Binding) { b.Namespace = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := Binding{ExecutionProfile: previous.ExecutionProfile, Workspace: previous.Workspace, Namespace: previous.Namespace, Name: "new"}
			if tc.modify != nil {
				tc.modify(&next)
			}
			usePreviousSession(&next, previous)
			require.Equal(t, previous.ID, next.PreviousBindingID)
			if tc.reuse {
				require.Equal(t, previous.SessionID, next.SessionID)
				require.Equal(t, previous.SessionUID, next.SessionUID)
				require.Equal(t, previous.Name, next.Name)
				require.Equal(t, previous.ID, next.CapabilityFileID)
			} else {
				require.Empty(t, next.SessionID)
				require.Empty(t, next.SessionUID)
				require.Empty(t, next.CapabilityFileID)
				require.Equal(t, "new", next.Name)
			}
		})
	}
}
