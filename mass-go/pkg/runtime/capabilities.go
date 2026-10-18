package runtime

// FeatureDefinition declares protocol support and the conditions that callers
// must satisfy. Support is not evidence that an operation is currently usable.
type FeatureDefinition struct {
	Name       string   `json:"name"`
	Support    string   `json:"support"`
	Conditions []string `json:"conditions"`
	Reason     string   `json:"reason,omitempty"`
}

// CapabilityAvailability is an observation of current local admission, not a
// guarantee that a native operation will succeed or identity will be verified.
type CapabilityAvailability struct {
	Name         string   `json:"name"`
	Support      string   `json:"support"`
	Availability string   `json:"availability"`
	Conditions   []string `json:"conditions"`
	Reason       string   `json:"reason,omitempty"`
}
