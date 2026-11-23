package github

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/superagent/internal/triggercontent"
	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
)

type githubDeliveryState struct {
	Accepted          uint64             `json:"accepted,omitempty"`
	Duplicates        uint64             `json:"duplicates,omitempty"`
	LastReceipt       coreclient.Receipt `json:"lastReceipt,omitzero"`
	LastDeliveryError string             `json:"lastDeliveryError,omitempty"`
	NextDeliveryAt    time.Time          `json:"nextDeliveryAt,omitzero"`
	DeliveryFailures  int                `json:"deliveryFailures,omitempty"`
	Backlog           int                `json:"-"`
}

type githubStreamState struct {
	EnabledAt     time.Time
	LastSuccessAt time.Time
	NextAttemptAt time.Time
	LastError     string
	Failures      int
	Items         map[string]githubRecord
	ItemCount     int
	Active        bool
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
	ThreadID               string
	Actor                  string
	Human                  bool
	SourceEventID          string
	At                     time.Time
	Meta                   map[string]any
}

func (o githubObservation) submission(c githubConfig, repo githubRepository, stream string, version uint64) (coreclient.TriggerSubmission, error) {
	key := fmt.Sprintf("%s\n%s\n%s\n%s\n%d", repo.Repository, stream, o.Key, o.Kind, version)
	id := fmt.Sprintf("github:%x", sha256.Sum256([]byte(key)))
	if o.Actor == "" {
		o.Actor = "github-trigger"
	}
	meta := make(map[string]any, len(o.Meta)+6)
	maps.Copy(meta, o.Meta)
	meta["repository"] = repo.Repository
	meta["observation"] = o.Kind
	meta["observationVersion"] = version
	meta["human"] = o.Human
	if o.SourceEventID != "" {
		meta["sourceEventId"] = o.SourceEventID
	}
	if c.InstanceID != "" {
		meta["collectorInstance"] = c.InstanceID
	}
	content := []events.ContentBlock{triggercontent.SourceText(githubMessageText(o.Title, o.Body, repo.Prompt), o.Actor, o.At)}
	prURL := ""
	if strings.HasPrefix(o.ThreadID, "https://") {
		prURL = o.ThreadID
		content = append(content, triggercontent.WebLink(prURL))
	}
	if o.URL != "" && o.URL != prURL {
		content = append(content, triggercontent.WebLink(o.URL))
	}
	diagnostics, err := json.Marshal(meta)
	if err != nil {
		return coreclient.TriggerSubmission{}, err
	}
	return coreclient.TriggerSubmission{OperationID: id, Message: coreclient.TriggerMessage{
		SourceMessageID: id, ThreadID: o.ThreadID, Content: content, Meta: diagnostics,
	}}, nil
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
