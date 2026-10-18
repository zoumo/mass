package managed

import (
	"encoding/json"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"sort"
)

// Projection only: this stores observed task snapshots, not an agent graph.
// Each source has independent replace semantics. Run IDs are part of identity.
// Finite local retention bounds do not bound the SDK's queues.
type backgroundProjection struct {
	scopes map[string]rt.TaskSnapshotPayload
}

const maxBackgroundTasks = 256
const maxBackgroundBytes = 1 << 20

func (b *backgroundProjection) apply(e rt.Event) error {
	if e.Task == nil && e.TaskSnapshot == nil {
		return nil
	}
	candidate := map[string]rt.TaskSnapshotPayload{}
	for k, v := range b.scopes {
		candidate[k] = v
	}
	if e.TaskSnapshot != nil {
		p := *e.TaskSnapshot
		p.Tasks = append([]rt.TaskPayload{}, p.Tasks...)
		candidate[p.SourceScope] = p
	}
	if e.Task != nil {
		t := *e.Task
		p, ok := candidate[t.SourceScope]
		if !ok {
			p = rt.TaskSnapshotPayload{Mode: "replace", SourceScope: t.SourceScope, Tasks: []rt.TaskPayload{}, Completeness: "partial"}
		}
		p.Tasks = append([]rt.TaskPayload{}, p.Tasks...)
		found := false
		for i, old := range p.Tasks {
			if old.TaskRef == t.TaskRef && old.Subject.RunID == t.Subject.RunID {
				if t.Background.State == "unknown" {
					t.Background = old.Background
				}
				if !t.StateObserved {
					t.State = old.State
					t.StateObserved = old.StateObserved
				}
				if t.Progress == nil {
					t.Progress = old.Progress
				}
				if t.Result == nil {
					t.Result = old.Result
				}
				p.Tasks[i] = t
				found = true
				break
			}
		}
		if !found {
			p.Tasks = append(p.Tasks, t)
		}
		candidate[t.SourceScope] = p
	}
	count := 0
	for _, p := range candidate {
		count += len(p.Tasks)
	}
	data, err := json.Marshal(candidate)
	if err != nil || count > maxBackgroundTasks || len(candidate) > maxBackgroundTasks || len(data) > maxBackgroundBytes {
		return rt.ErrBackpressure
	}
	// Deep copy detaches projection storage from mutable public event payloads.
	if err = json.Unmarshal(data, &candidate); err != nil {
		return err
	}
	b.scopes = candidate
	return nil
}
func (b *backgroundProjection) snapshot() rt.BackgroundSummary {
	result := rt.BackgroundSummary{Completeness: "unknown"}
	keys := []string{}
	for k := range b.scopes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := b.scopes[k]
		result.Scopes = append(result.Scopes, p)
		result.Tasks = append(result.Tasks, p.Tasks...)
	}
	if len(keys) > 0 {
		result.Completeness = "partial"
	}
	// Per-source completeness is retained; a complete root inventory does not
	// prove that every child source has been observed.
	data, _ := json.Marshal(result)
	_ = json.Unmarshal(data, &result)
	return result
}
