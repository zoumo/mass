package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type textEmotion struct {
	ID           string `json:"emotionId" yaml:"emotionId"`
	BackgroundID string `json:"backgroundId" yaml:"backgroundId"`
	Name         string `json:"name" yaml:"name"`
	Text         string `json:"text" yaml:"text"`
}

func (e textEmotion) valid() bool {
	return validNativeID(e.ID) && validNativeID(e.BackgroundID) &&
		strings.TrimSpace(e.Name) != "" && !strings.ContainsRune(e.Name, 0) &&
		strings.TrimSpace(e.Text) != "" && !strings.ContainsRune(e.Text, 0)
}

func (e textEmotion) args() []string {
	return []string{"--emotion-id", e.ID, "--emotion-name", e.Name, "--text", e.Text, "--background-id", e.BackgroundID}
}

type feedbackConfig struct {
	StatePath string      `yaml:"statePath"`
	Thinking  textEmotion `yaml:"thinking"`
	Done      textEmotion `yaml:"done"`
}

func (c feedbackConfig) validate() error {
	if !filepath.IsAbs(c.StatePath) || !c.Thinking.valid() || !c.Done.valid() || c.Thinking.ID == c.Done.ID {
		return errors.New("feedback requires an absolute statePath and two distinct complete text emotion templates")
	}
	return nil
}

const (
	feedbackAwaiting   = "awaiting_admission"
	feedbackReady      = "ready"
	feedbackThinking   = "thinking"
	feedbackDone       = "done"
	feedbackSuppressed = "suppressed"
)

type feedbackRecord struct {
	Channel    Channel     `json:"channel"`
	ObservedAt time.Time   `json:"observedAt"`
	Thinking   textEmotion `json:"thinking"`
	Done       textEmotion `json:"done"`
	Phase      string      `json:"phase"`
	OutboxIDs  []string    `json:"outboxIds,omitempty"`
}

type feedbackState struct {
	Version  int                       `json:"version"`
	Profile  string                    `json:"profile"`
	Source   string                    `json:"source"`
	Instance string                    `json:"instanceId"`
	Records  map[string]feedbackRecord `json:"records"`
}

type feedbackWorker struct {
	config Config
	core   *CoreClient
	log    func(string, string)
	wake   chan struct{}
	mu     sync.Mutex
	state  feedbackState
}

func openFeedback(c Config, core *CoreClient, log func(string, string)) (*feedbackWorker, func(), error) {
	if c.Feedback == nil {
		return nil, func() {}, nil
	}
	path := c.Feedback.StatePath
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, errors.New("cannot create DWS feedback state directory")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, errors.New("cannot open DWS feedback state lock")
	}
	closeLock := func() { _ = lock.Close() }
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeLock()
		return nil, nil, errors.New("cannot acquire DWS feedback state lock")
	}
	state := feedbackState{Version: 1, Profile: c.Profile, Source: c.Source, Instance: c.InstanceID, Records: map[string]feedbackRecord{}}
	file, err := os.Open(path)
	switch {
	case err == nil:
		defer file.Close()
		decoder := json.NewDecoder(file)
		err = decoder.Decode(&state)
		if err != nil || decoder.Decode(new(any)) != io.EOF || !state.validFor(c) {
			closeLock()
			return nil, nil, errors.New("invalid or mismatched DWS feedback state")
		}
	case !errors.Is(err, os.ErrNotExist):
		closeLock()
		return nil, nil, errors.New("cannot read DWS feedback state")
	}
	w := &feedbackWorker{config: c, core: core, log: log, wake: make(chan struct{}, 1), state: state}
	if err := w.save(); err != nil {
		closeLock()
		return nil, nil, errors.New("cannot initialize DWS feedback state")
	}
	return w, closeLock, nil
}

func (s feedbackState) validFor(c Config) bool {
	if s.Version != 1 || s.Profile != c.Profile || s.Source != c.Source || s.Instance != c.InstanceID || s.Records == nil {
		return false
	}
	for key, record := range s.Records {
		switch record.Phase {
		case feedbackAwaiting, feedbackReady, feedbackThinking, feedbackDone, feedbackSuppressed:
		default:
			return false
		}
		if key != feedbackKey(record.Channel) || record.ObservedAt.IsZero() || !record.Thinking.valid() ||
			!record.Done.valid() || record.Thinking.ID == record.Done.ID {
			return false
		}
	}
	return true
}

func (w *feedbackWorker) save() error {
	path := w.config.Feedback.StatePath
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	file, err := os.CreateTemp(dir.Name(), ".dws-feedback-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := json.NewEncoder(file).Encode(w.state); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return dir.Sync()
}

func feedbackKey(c Channel) string {
	return c.ConversationID + "\x00" + c.MessageID
}

func (w *feedbackWorker) notify() {
	if w != nil {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (w *feedbackWorker) remember(message Message, observedAt time.Time) error {
	if w == nil || message.Channel == nil {
		return nil
	}
	channel := *message.Channel
	if _, allowed := replyTarget(w.config, channel); !allowed {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	key := feedbackKey(channel)
	if _, found := w.state.Records[key]; found {
		return nil
	}
	w.state.Records[key] = feedbackRecord{
		Channel: channel, ObservedAt: observedAt,
		Thinking: w.config.Feedback.Thinking, Done: w.config.Feedback.Done, Phase: feedbackAwaiting,
	}
	if err := w.save(); err != nil {
		delete(w.state.Records, key)
		return err
	}
	return nil
}

func (w *feedbackWorker) recordAdmission(message Message, receipt Receipt) error {
	if w == nil || message.Channel == nil {
		return nil
	}
	key := feedbackKey(*message.Channel)
	w.mu.Lock()
	record, found := w.state.Records[key]
	w.mu.Unlock()
	if !found || record.Phase != feedbackAwaiting {
		return nil
	}
	if err := w.setPhase(key, feedbackAwaiting, admittedFeedback(record, receipt, receipt.Duplicate || receipt.Status == "duplicate")); err != nil {
		return err
	}
	w.notify()
	return nil
}

func admittedFeedback(record feedbackRecord, receipt Receipt, checkAge bool) string {
	// A duplicate can be the receipt for our own lost submission. Only older
	// admissions predate the durable local intent and must stay untouched. A
	// fresh POST receipt needs no comparison between the Core and Trigger clocks.
	if checkAge {
		accepted, err := time.Parse(time.RFC3339Nano, receipt.AcceptedAt)
		if err != nil || accepted.Before(record.ObservedAt) {
			return feedbackSuppressed
		}
	}
	switch receipt.EchoStatus {
	case "outbound_echo":
		return feedbackSuppressed
	case "", "cleared", "released":
		return feedbackReady
	default:
		return feedbackAwaiting
	}
}

func (w *feedbackWorker) trackReply(channel Channel, id string) error {
	if w == nil {
		return nil
	}
	key := feedbackKey(channel)
	w.mu.Lock()
	defer w.mu.Unlock()
	record, found := w.state.Records[key]
	if !found || record.Phase == feedbackDone || record.Phase == feedbackSuppressed || slices.Contains(record.OutboxIDs, id) {
		return nil
	}
	original := record
	record.OutboxIDs = append(record.OutboxIDs, id)
	w.state.Records[key] = record
	if err := w.save(); err != nil {
		w.state.Records[key] = original
		return err
	}
	return nil
}

func (w *feedbackWorker) setPhase(key, previous, next string) error {
	if next == previous {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	record := w.state.Records[key]
	if record.Phase != previous {
		return nil
	}
	original := record
	record.Phase = next
	if next == feedbackDone || next == feedbackSuppressed {
		record.OutboxIDs = nil
	}
	w.state.Records[key] = record
	if err := w.save(); err != nil {
		w.state.Records[key] = original
		return err
	}
	if next == feedbackThinking || next == feedbackDone {
		w.log("feedback_"+next, "")
	}
	return nil
}

func (w *feedbackWorker) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		w.mu.Lock()
		var pending []feedbackRecord
		for _, record := range w.state.Records {
			if record.Phase == feedbackAwaiting || record.Phase == feedbackReady ||
				(record.Phase == feedbackThinking && len(record.OutboxIDs) > 0) {
				pending = append(pending, record)
			}
		}
		w.mu.Unlock()
		slices.SortFunc(pending, func(a, b feedbackRecord) int { return a.ObservedAt.Compare(b.ObservedAt) })
		for _, record := range pending {
			if ctx.Err() != nil {
				return
			}
			next := w.advance(ctx, record)
			if err := w.setPhase(feedbackKey(record.Channel), record.Phase, next); err != nil {
				w.log("feedback_state_failed", "save_phase")
			}
		}
		select {
		case <-ctx.Done():
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

func (w *feedbackWorker) advance(ctx context.Context, record feedbackRecord) string {
	if _, allowed := replyTarget(w.config, record.Channel); !allowed {
		return record.Phase
	}
	if record.Phase == feedbackAwaiting {
		raw, code, _ := w.core.request(ctx, http.MethodGet, "/v1/channel/messages?sourceMessageId="+url.QueryEscape(record.Channel.MessageID), nil, 65536)
		var receipt Receipt
		if code != "" || json.Unmarshal(raw, &receipt) != nil || receipt.MessageID == "" {
			return feedbackAwaiting
		}
		phase := admittedFeedback(record, receipt, true)
		if phase != feedbackReady {
			return phase
		}
	}
	delivered := w.replyDelivered(ctx, record)
	if record.Phase == feedbackThinking && !delivered {
		return feedbackThinking
	}
	dwsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	thinking, done, known := readEmotions(dwsCtx, w.config, record)
	if !known {
		w.log("feedback_reaction_failed", "readback")
		return record.Phase
	}
	if done && delivered && !thinking {
		return feedbackDone
	}
	if thinking && !delivered {
		return feedbackThinking
	}
	if delivered && thinking {
		args := []string{
			"chat", "message", "update-text-emotion", "--conversation-id", record.Channel.ConversationID,
			"--message-id", record.Channel.MessageID, "--old-emotion-id", record.Thinking.ID,
		}
		_, _ = dwsCall(dwsCtx, w.config, append(args, record.Done.args()...)...)
	} else if !thinking && !done {
		emotion := record.Thinking
		if delivered {
			emotion = record.Done
		}
		args := []string{
			"chat", "+messages-add-text-emotion", "--conversation-id", record.Channel.ConversationID,
			"--msg-id", record.Channel.MessageID, "--yes",
		}
		_, _ = dwsCall(dwsCtx, w.config, append(args, emotion.args()...)...)
	}
	thinking, done, known = readEmotions(dwsCtx, w.config, record)
	if known && done && delivered && !thinking {
		return feedbackDone
	}
	if known && thinking {
		return feedbackThinking
	}
	w.log("feedback_reaction_failed", "write_unconfirmed")
	return record.Phase
}

func (w *feedbackWorker) replyDelivered(ctx context.Context, record feedbackRecord) bool {
	var terminalFailures []string
	for _, id := range record.OutboxIDs {
		raw, code, _ := w.core.request(ctx, http.MethodGet, outboxPath+"/items/"+url.PathEscape(id), nil, 65536)
		var item outboxItem
		if code != "" || json.Unmarshal(raw, &item) != nil || item.ID != id || item.Receipt == nil {
			continue
		}
		if item.Status == "sent" && item.Receipt.ConversationID == record.Channel.ConversationID &&
			validNativeID(item.Receipt.MessageID) {
			return true
		}
		if item.Status == "failed" && item.Receipt.RetryAt == "" {
			terminalFailures = append(terminalFailures, id)
		}
	}
	if len(terminalFailures) > 0 {
		w.forgetFailedReplies(record.Channel, terminalFailures)
	}
	return false
}

func (w *feedbackWorker) forgetFailedReplies(channel Channel, failures []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := feedbackKey(channel)
	record := w.state.Records[key]
	original := record
	record.OutboxIDs = slices.DeleteFunc(slices.Clone(record.OutboxIDs), func(id string) bool {
		return slices.Contains(failures, id)
	})
	w.state.Records[key] = record
	if err := w.save(); err != nil {
		w.state.Records[key] = original
		w.log("feedback_state_failed", "forget_failed_replies")
	}
}

func readEmotions(ctx context.Context, c Config, record feedbackRecord) (thinking, done, known bool) {
	result, ok := dwsCall(ctx, c, "chat", "message", "list-emotion-replies", "--msg-ids", record.Channel.MessageID)
	var body struct {
		Messages []struct {
			MessageID string `json:"openMessageId"`
			Reactions []struct {
				ID           string `json:"emotionId"`
				BackgroundID string `json:"backgroundId"`
			} `json:"emotionReplyList"`
		} `json:"messages"`
	}
	if !ok || result.Success == nil || !*result.Success || json.Unmarshal(result.Result, &body) != nil ||
		len(body.Messages) != 1 || body.Messages[0].MessageID != record.Channel.MessageID || body.Messages[0].Reactions == nil {
		return false, false, false
	}
	for _, reaction := range body.Messages[0].Reactions {
		switch {
		case reaction.ID == record.Thinking.ID && reaction.BackgroundID == record.Thinking.BackgroundID:
			thinking = true
		case reaction.ID == record.Done.ID && reaction.BackgroundID == record.Done.BackgroundID:
			done = true
		}
	}
	return thinking, done, true
}
