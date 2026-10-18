package runtime

// Interaction is serializable specification, never a native response capability.
type Interaction struct {
	Ref       string      `json:"interactionRef"`
	Kind      string      `json:"kind"`
	State     string      `json:"state"`
	Prompt    string      `json:"prompt,omitempty"`
	ToolName  string      `json:"toolName,omitempty"`
	Input     *ContentSet `json:"input,omitempty"`
	CallRef   string      `json:"callRef,omitempty"`
	Decisions []string    `json:"decisions,omitempty"`
	Questions []Question  `json:"questions,omitempty"`
}
type Question struct {
	ID       string           `json:"questionRef"`
	Prompt   string           `json:"prompt"`
	Header   string           `json:"header,omitempty"`
	Options  []QuestionOption `json:"options"`
	Multiple bool             `json:"multiple"`
	Required *bool            `json:"required,omitempty"`
	FreeText *bool            `json:"freeText,omitempty"`
	Secret   *bool            `json:"secret,omitempty"`
}
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
type Answer struct {
	Text       *string  `json:"text,omitempty"`
	Selections []string `json:"selections"`
}
type Response struct {
	Decision string            `json:"decision,omitempty"`
	Answers  map[string]Answer `json:"answers,omitempty"`
}
type ResponseReceipt struct {
	OperationRef     string      `json:"operationRef,omitempty"`
	Key              SessionKey  `json:"key"`
	InteractionRef   string      `json:"interactionRef"`
	Attempt          string      `json:"attempt"`
	Outcome          string      `json:"outcome"`
	HandedToSDK      Observation `json:"handedToSDK"`
	WriteCompleted   Observation `json:"writeCompleted"`
	NativeResolution Observation `json:"nativeResolution"`
	Failure          *SafeError  `json:"failure,omitempty"`
}

// Validate checks the exact response form before authority is consumed.
func (i Interaction) Validate(r Response) error {
	if i.Kind == "permission" {
		if len(r.Answers) > 0 {
			return ErrInvalidInput
		}
		for _, d := range i.Decisions {
			if d == r.Decision {
				return nil
			}
		}
		return ErrUnsupported
	}
	if i.Kind != "question" || r.Decision != "" {
		return ErrInvalidInput
	}
	known := map[string]Question{}
	for _, q := range i.Questions {
		known[q.ID] = q
		if q.Required != nil && *q.Required {
			if _, ok := r.Answers[q.ID]; !ok {
				return ErrInvalidInput
			}
		}
	}
	if len(r.Answers) == 0 {
		return ErrInvalidInput
	}
	for id, a := range r.Answers {
		q, ok := known[id]
		if !ok || (a.Text == nil) == (a.Selections == nil) {
			return ErrInvalidInput
		}
		if a.Text != nil {
			if len(*a.Text) > 1<<20 || (q.FreeText == nil || !*q.FreeText) {
				return ErrInvalidInput
			}
		} else {
			if !q.Multiple && len(a.Selections) != 1 {
				return ErrInvalidInput
			}
			seen := map[string]bool{}
			for _, v := range a.Selections {
				found := false
				for _, o := range q.Options {
					if o.Label == v {
						found = true
					}
				}
				if !found || seen[v] {
					return ErrInvalidInput
				}
				seen[v] = true
			}
		}
	}
	return nil
}

// InteractionSummary is a current attachment observation, not durable inventory.
type InteractionSummary struct {
	Pending     int `json:"pending"`
	Consumed    int `json:"consumed"`
	Invalidated int `json:"invalidated"`
}
