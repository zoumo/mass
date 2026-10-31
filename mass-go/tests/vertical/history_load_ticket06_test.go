package vertical_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

func TestTicket06CodexPagedLoadAndSameConnectionInput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "history-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/history-load-peer").CombinedOutput()
	require.NoError(t, err, string(out))
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	defer st.Close()
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: t.TempDir()}}))
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"LOAD_CWD": cwd}}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(c))
	}()
	receipt, err := svc.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: cwd, SessionID: "native-thread"})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: "native-thread"}
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.HistoryLoad.Phase == "completed" }, 5*time.Second, time.Millisecond, "load %s never completed", receipt.LoadID)
	page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 3)
	require.Equal(t, []string{"first", "answer", "last"}, []string{page.Events[0].ItemID, page.Events[1].ItemID, page.Events[2].ItemID})
	require.Equal(t, "turn-z", page.Events[0].Replay.NativeParentID)
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("continue")}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		p, e := svc.ListHistoryEvents(ref, store.HistoryQuery{})
		return e == nil && len(p.Events) == 4
	}, time.Second, time.Millisecond)
	_, err = svc.CancelHistoryLoad(ctx(t), ref, receipt.LoadID)
	require.Error(t, err, "stale cancellation must not close continued work")
}
