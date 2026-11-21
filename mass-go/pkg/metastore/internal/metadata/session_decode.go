package metadata

import (
	"encoding/json"

	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

// storedSession accepts the removed Turn evidence only when reading metadata
// written by an older daemon. All other properties are still checked strictly.
type storedSession resource.Session

func (r *storedSession) UnmarshalJSON(b []byte) error {
	wire := struct {
		*resource.Session
		Status struct {
			*resource.SessionStatus
			RemovedTurnEvidence json.RawMessage `json:"reconciledTurn"`
		} `json:"status"`
	}{Session: (*resource.Session)(r)}
	wire.Status.SessionStatus = &r.Status
	return fileio.Decode(b, &wire)
}
