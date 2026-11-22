package triggers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type githubDeliveryState struct {
	Accepted          uint64    `json:"accepted,omitempty"`
	Duplicates        uint64    `json:"duplicates,omitempty"`
	LastReceipt       Receipt   `json:"lastReceipt,omitzero"`
	LastDeliveryError string    `json:"lastDeliveryError,omitempty"`
	NextDeliveryAt    time.Time `json:"nextDeliveryAt,omitzero"`
	DeliveryFailures  int       `json:"deliveryFailures,omitempty"`
	Backlog           int       `json:"-"`
}

type githubStreamState struct {
	DiscussionPriority bool                    `json:"discussionPriority,omitempty"`
	DiscussionClosed   bool                    `json:"discussionClosed,omitempty"`
	EnabledAt          time.Time               `json:"enabledAt"`
	LastSuccessAt      time.Time               `json:"lastSuccessAt,omitzero"`
	LastFullAt         time.Time               `json:"lastFullAt,omitzero"`
	NextAttemptAt      time.Time               `json:"nextAttemptAt,omitzero"`
	LastError          string                  `json:"lastError,omitempty"`
	Failures           int                     `json:"failures,omitempty"`
	Items              map[string]githubRecord `json:"items"`
	ItemCount          int                     `json:"-"`
	Active             bool                    `json:"-"`
	SilentBaseline     bool                    `json:"-"`
	MatchKey           string                  `json:"-"`
	Match              githubMatch             `json:"-"`
}

type githubRecord struct {
	Version   uint64          `json:"version"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Data      json.RawMessage `json:"data"`
}

// A scope returns all its observations only after the complete read succeeds.
// A blank kind stores the record without generating a message.
type githubObservation struct {
	Key                    string
	UpdatedAt              time.Time
	Data                   json.RawMessage
	Kind, Title, Body, URL string
	Thread                 ThreadRef
	Actor                  string
	Human                  bool
	SourceEventID          string
	At                     time.Time
	Meta                   map[string]any
	Excluded, Rebaseline   bool
}

func (o githubObservation) submission(c githubConfig, repo githubRepository, stream string, version uint64) Submission {
	key := fmt.Sprintf("%s\n%s\n%s\n%s\n%d", repo.Repository, stream, o.Key, o.Kind, version)
	id := fmt.Sprintf("github:%x", sha256.Sum256([]byte(key)))
	if o.Actor == "" {
		o.Actor = "github-trigger"
	}
	if o.Meta == nil {
		o.Meta = make(map[string]any)
	}
	o.Meta["repository"] = repo.Repository
	o.Meta["observation"] = o.Kind
	o.Meta["observationVersion"] = version
	return Submission{OperationID: id, Message: Message{
		Source: githubSource, AccountID: c.AccountID, CollectorInstance: c.InstanceID,
		ThreadRef: o.Thread, SourceMessageID: id, Actor: o.Actor, Human: &o.Human,
		Text: githubMessageText(o.Title, o.Body, repo.Prompt), At: o.At.UTC().Format(time.RFC3339Nano), URL: o.URL,
		Meta: o.Meta, SourceEventID: o.SourceEventID,
	}}
}

var githubTextEscaper = strings.NewReplacer(
	"<github-source>", "&lt;github-source&gt;", "</github-source>", "&lt;/github-source&gt;",
	"<repository-context>", "&lt;repository-context&gt;", "</repository-context>", "&lt;/repository-context&gt;",
)

func githubMessageText(title, body, prompt string) string {
	text := "<github-source>\n" + githubTextEscaper.Replace(title+"\n\n"+body) + "\n</github-source>"
	if strings.TrimSpace(prompt) != "" {
		text += "\n\n<repository-context>\n仓库配置补充上下文\n" + githubTextEscaper.Replace(prompt) + "\n</repository-context>"
	}
	return text
}
