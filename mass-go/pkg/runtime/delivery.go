package runtime

import "time"

// DeliveryLimits bound retained common events, independently of SDK queues.
// Previews use the same lossless admission policy as complete facts.
type DeliveryLimits struct {
	Facts int `json:"facts,omitempty"`
	Bytes int `json:"bytes,omitempty"`
}

func (l DeliveryLimits) Effective() DeliveryLimits {
	if l.Facts == 0 {
		l.Facts = 256
	}
	if l.Bytes == 0 {
		l.Bytes = 4 << 20
	}
	return l
}

type StopPolicy string

const (
	StopDrain StopPolicy = "drain"
	StopAbort StopPolicy = "abort"
)

type StopOptions struct {
	Policy StopPolicy `json:"policy"`
}
type StopReceipt struct {
	RuntimeName         string      `json:"runtimeName"`
	AdmissionClosed     bool        `json:"admissionClosed"`
	IncompleteFacts     bool        `json:"incompleteFacts"`
	OwnedResources      Observation `json:"ownedResources"`
	RemainingSessions   int         `json:"remainingSessions"`
	RemainingOperations int         `json:"remainingOperations"`
	Outcome             string      `json:"outcome"`
}

const DefaultStopTimeout = 5 * time.Second
