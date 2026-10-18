package runtime

// InputCommand accepts one explicit text block, including empty text. Rich
// content and options are rejected before SDK handoff. Text is bounded to 1 MiB.
type InputCommand struct {
	Blocks  []ContentBlock    `json:"blocks"`
	Options map[string]string `json:"options,omitempty"`
}

func TextInput(text string) InputCommand {
	return InputCommand{Blocks: []ContentBlock{{Kind: "text", Text: &text}}}
}
func (i InputCommand) Text() (string, error) {
	if len(i.Options) > 0 {
		return "", ErrUnsupported
	}
	if len(i.Blocks) != 1 {
		return "", ErrInvalidInput
	}
	b := i.Blocks[0]
	if b.Kind != "text" {
		return "", ErrUnsupported
	}
	if b.Text == nil || len(*b.Text) > 1<<20 || len(b.JSON) > 0 || b.Descriptor != "" || b.Namespace != "" || b.BlockRef != "" || b.Index != nil || b.Stage != "" || b.Completeness != "" {
		return "", ErrInvalidInput
	}
	return *b.Text, nil
}

type ForegroundState struct {
	State        string `json:"state"`
	OperationRef string `json:"operationRef,omitempty"`
	WorkRef      string `json:"workRef,omitempty"`
}

// Unknown background observation is independent of foreground completion.
type BackgroundSummary struct {
	Tasks        []TaskPayload         `json:"tasks,omitempty"`
	Scopes       []TaskSnapshotPayload `json:"scopes,omitempty"`
	Completeness string                `json:"completeness"`
}
type SubmissionEvidence struct {
	HandedToSDK        Observation `json:"handedToSDK"`
	WriteCompleted     Observation `json:"writeCompleted"`
	NativeAcknowledged Observation `json:"nativeAcknowledged"`
	NativeRecorded     Observation `json:"nativeRecorded"`
	Completed          Observation `json:"completed"`
}
type Submission struct {
	OperationRef string     `json:"operationRef"`
	Key          SessionKey `json:"key"`
	Outcome      string     `json:"outcome"`
	WorkRef      string     `json:"workRef,omitempty"`
	SubmissionEvidence
	Failure *SafeError `json:"failure,omitempty"`
}
type Cancellation struct {
	OperationRef       string      `json:"operationRef"`
	Key                SessionKey  `json:"key"`
	TargetOperationRef string      `json:"targetOperationRef"`
	WorkRef            string      `json:"workRef,omitempty"`
	Outcome            string      `json:"outcome"`
	Requested          Observation `json:"requested"`
	NativeAcknowledged Observation `json:"nativeAcknowledged"`
	TargetFinished     Observation `json:"targetFinished"`
	Failure            *SafeError  `json:"failure,omitempty"`
}

func UnknownSubmission() Submission {
	return Submission{SubmissionEvidence: SubmissionEvidence{HandedToSDK: UnknownObservation(), WriteCompleted: UnknownObservation(), NativeAcknowledged: UnknownObservation(), NativeRecorded: UnknownObservation(), Completed: UnknownObservation()}}
}
