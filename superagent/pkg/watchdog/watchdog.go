// Package watchdog runs a single Superagent business check outside Core.
package watchdog

import (
	"context"
	"errors"
	"os"
	"time"
)

const (
	DefaultEndpoint = "http://127.0.0.1:24370"
	DefaultCodexBin = "codex"
	DefaultTimeout  = 2 * time.Minute
	reportAge       = 30 * time.Minute
)

type Options struct {
	Endpoint string
	CodexBin string
	Timeout  time.Duration
	// Compose provides an owned workspace root and a private result stream.
	WorkDir       string
	ProcessOutput *os.File
}

type Result struct {
	ObservedAt         time.Time `json:"observedAt"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	ReleaseUnconfirmed bool      `json:"releaseUnconfirmed,omitempty"`
	*Delivery
	Judgment       string `json:"judgment,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
}

type Delivery struct {
	IssueID      string    `json:"issueId"`
	TopicID      string    `json:"topicId"`
	GoalRevision int       `json:"goalRevision"`
	ReportID     string    `json:"reportId"`
	EventID      string    `json:"eventId"`
	SentAt       time.Time `json:"sentAt"`
}

// Run reads the current state and judges at most one candidate. Status and
// reason describe the outcome of the round, including operational failures.
func (o Options) Run(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	result := Result{ObservedAt: time.Now().UTC(), Status: "incomplete"}
	core, err := newReader(o.Endpoint)
	if err != nil {
		result.Reason = "invalid_endpoint"
		return result
	}
	defer core.client.CloseIdleConnections()
	evidence, err := core.collect(ctx, result.ObservedAt)
	if err != nil {
		result.Reason = failureReason(ctx.Err(), "core_read_failed")
		return result
	}
	if evidence == nil {
		result.Status, result.Reason = "no_finding", "no_candidate"
		return result
	}

	result.Delivery = &evidence.Delivery
	assessment, err, releaseUnconfirmed := judge(ctx, o, evidence)
	result.ReleaseUnconfirmed = releaseUnconfirmed
	switch {
	case errors.Is(err, errInvalidOutput):
		result.Reason = failureReason(ctx.Err(), "invalid_output")
	case err != nil:
		result.Reason = failureReason(errors.Join(ctx.Err(), err), "agent_failed")
	case releaseUnconfirmed:
		result.Reason = "release_unconfirmed"
	default:
		result.Judgment, result.Recommendation = assessment.Judgment, assessment.Recommendation
		result.Reason = assessment.Outcome
		switch assessment.Outcome {
		case "suspected_missed_acceptance":
			result.Status = "finding"
		case "normal_waiting":
			result.Status = "no_finding"
		}
	}
	return result
}

func failureReason(err error, fallback string) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return fallback
	}
}
