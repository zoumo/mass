package dws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
	"github.com/zoumo/mass/superagent/triggers/internal/trigger"
)

type statusSnapshot struct {
	Source            string `json:"source"`
	AccountID         string `json:"accountId"`
	InstanceID        string `json:"instanceId"`
	Connection        string `json:"connection"`
	ConsumerReady     bool   `json:"consumerReady"`
	ReadyConsumers    int    `json:"readyConsumers"`
	Consumers         int    `json:"consumers"`
	Admission         string `json:"admission"`
	Backlog           int    `json:"backlog"`
	Held              int    `json:"held"`
	OutboundEchoes    int    `json:"outboundEchoes"`
	PermanentFailures int    `json:"permanentFailures"`
	Capacity          int    `json:"capacity"`
	LastReceivedAt    string `json:"lastReceivedAt,omitempty"`
	LastErrorAt       string `json:"lastErrorAt,omitempty"`
	LastError         string `json:"lastError,omitempty"`
	StopReason        string `json:"stopReason,omitempty"`
	Accepted          int    `json:"accepted"`
	Duplicates        int    `json:"duplicates"`
	Rejected          int    `json:"rejected"`
	Unmatched         int    `json:"unmatched"`
	OwnDMFiltered     int    `json:"ownDirectFiltered"`
}

type state struct {
	sync.RWMutex
	snapshot statusSnapshot
}

func (s *state) get() statusSnapshot {
	s.RLock()
	defer s.RUnlock()
	return s.snapshot
}

func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *state) update(fn func(*statusSnapshot)) {
	s.Lock()
	defer s.Unlock()
	fn(&s.snapshot)
}

func run(ctx context.Context, c dwsConfig, core *coreclient.Client, log io.Writer) (runErr error) {
	closeOwner, err := lockDWSOwner(c)
	if err != nil {
		return err
	}
	defer closeOwner()
	queue, closeQueue, err := openDWSQueue(c)
	if err != nil {
		return err
	}
	defer closeQueue()
	evidence, err := openSendEvidence(c.DatabasePath)
	if err != nil {
		return err
	}
	backlog, failed, err := queue.counts()
	if err != nil {
		return errors.New("database_read_failed")
	}
	targets := c.subscriptions()
	st := &state{snapshot: statusSnapshot{
		Source: c.Source, AccountID: c.AccountID, InstanceID: c.InstanceID,
		Connection: "connecting", Consumers: len(targets), Capacity: c.Capacity, Admission: "waiting",
		Backlog: backlog, PermanentFailures: failed,
	}}
	if backlog > 0 {
		st.snapshot.Connection = "recovering"
		st.snapshot.Admission = "pending"
	}
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", c.Listen)
	if err != nil {
		return errors.New("cannot bind status listener")
	}
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		trigger.WriteDiagnostic(json.NewEncoder(w), st.get())
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	enc := json.NewEncoder(log)
	var logMu sync.Mutex
	emit := func(kind, reason string) {
		logMu.Lock()
		defer logMu.Unlock()
		trigger.WriteDiagnostic(enc, map[string]any{"event": kind, "reason": reason, "state": st.get()})
	}
	feedback, closeFeedback, err := openFeedback(c, core, evidence, emit)
	if err != nil {
		return err
	}
	defer closeFeedback()
	statusEndpoint := url.URL{Scheme: "http", Host: listener.Addr().String()}
	if err := enc.Encode(map[string]string{"status": "starting", "statusEndpoint": statusEndpoint.String(), "consumerState": st.get().Connection}); err != nil {
		return err
	}
	if feedback != nil {
		feedbackCtx, stopFeedback := context.WithCancel(ctx)
		feedbackDone := make(chan struct{})
		go func() {
			defer close(feedbackDone)
			feedback.run(feedbackCtx)
		}()
		defer func() {
			stopFeedback()
			<-feedbackDone
		}()
	}
	egressCtx, stopEgress := context.WithCancel(ctx)
	defer stopEgress()
	drain, cancelDrain := context.WithCancel(context.Background())
	egressDone := make(chan struct{})
	egressStarted := c.repliesEnabled()
	if egressStarted {
		go func() {
			defer close(egressDone)
			egress{config: c, core: core, evidence: evidence, log: emit, feedback: feedback}.run(egressCtx, drain)
		}()
	}
	defer func() {
		stopEgress()
		defer cancelDrain()
		if !egressStarted {
			return
		}
		select {
		case <-egressDone:
		case <-time.After(5 * time.Second):
			cancelDrain()
			<-egressDone
			emit("outbox_shutdown_unconfirmed", "deadline")
		}
	}()
	consumerCtx, cancelConsumers := context.WithCancel(context.Background())
	defer cancelConsumers()
	incoming := make(chan input)
	var consumers sync.WaitGroup
	var startupDeadline <-chan time.Time
	var startupTimer *time.Timer
	defer func() {
		if startupTimer != nil {
			startupTimer.Stop()
		}
	}()
	startConsumers := func() {
		st.update(func(s *statusSnapshot) { s.Connection = "connecting" })
		startupTimer = time.NewTimer(15 * time.Second)
		startupDeadline = startupTimer.C
		for i, target := range targets {
			consumers.Add(1)
			go consume(consumerCtx, c, i, target, incoming, &consumers)
		}
	}
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	results := make(chan coreclient.SubmitResult, 1)
	var head dwsQueueItem
	active := false
	ready := make(map[int]bool)
	stopping := false
	fatalReason := ""
	var deadline <-chan time.Time
	var retry <-chan time.Time
	backoff := time.Duration(c.RetryMillis) * time.Millisecond
	rootDone := ctx.Done()
	stop := func(reason string) {
		if stopping {
			return
		}
		stopping = true
		fatalReason = reason
		stopEgress()
		cancelConsumers()
		st.update(func(s *statusSnapshot) {
			s.ConsumerReady = false
			s.ReadyConsumers = 0
			s.Connection = "disconnected"
			s.StopReason = reason
			if reason != "shutdown" && s.Admission != "failed" {
				s.LastError = reason
				s.LastErrorAt = stamp()
			}
		})
		emit("consumer_stopped", reason)
	}
	queueFailed := func(reason string) error {
		st.update(func(s *statusSnapshot) { s.Admission = "failed"; s.LastError = reason; s.LastErrorAt = stamp() })
		emit("queue_storage_failed", reason)
		stop(reason)
		return errors.New(reason)
	}
	trackFeedback := func(message queuedSubmission, observedAt time.Time) error {
		if err := feedback.remember(message.nativeReply(), observedAt); err != nil {
			emit("feedback_state_failed", "track_admission")
			stop("feedback_state_failed")
			return errors.New("cannot track DWS feedback before admission")
		}
		return nil
	}
	defer func() {
		cancelConsumers()
		done := make(chan struct{})
		go func() { consumers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			emit("consumer_shutdown_unknown", "consumer_did_not_exit_after_sigterm")
			runErr = errors.New("consumer shutdown unconfirmed after SIGTERM; inspect own consumer before restart")
		}
	}()
	for {
		allDeferred := false
		if backlog > 0 && !active && retry == nil {
			held := 0
			for cursor := int64(0); !active; {
				items, err := queue.pending(cursor)
				if err != nil {
					return queueFailed("database_read_failed")
				}
				for _, item := range items {
					cursor = item.sequence
					decision, outboxID := item.classify(evidence)
					switch decision {
					case "held":
						held++
						continue
					case "outbound_echo":
						if err := queue.finish(item.sequence, "outbound_echo"); err != nil {
							return queueFailed("database_write_failed")
						}
						backlog--
						st.update(func(s *statusSnapshot) { s.Backlog = backlog; s.OutboundEchoes++ })
						emit("outbound_echo", "confirmed_native_identity")
						continue
					}
					item.submission, err = queue.prepare(item, outboxID)
					if err != nil {
						return queueFailed("database_write_failed")
					}
					payload, err := json.Marshal(item.submission.TriggerSubmission)
					if err != nil {
						return queueFailed("database_read_failed")
					}
					if err := trackFeedback(item.submission, item.observedAt); err != nil {
						return err
					}
					head = item
					active = true
					go func(payload []byte) { results <- core.SubmitPayload(requestCtx, payload) }(payload)
					break
				}
				if len(items) < dwsQueueReadLimit {
					break
				}
			}
			allDeferred = !active && held > 0
			st.update(func(s *statusSnapshot) { s.Held = held })
			if allDeferred {
				retry = time.After(250 * time.Millisecond)
			}
		}
		if stopping && (backlog == 0 || allDeferred) {
			if fatalReason == "shutdown" {
				return nil
			}
			return errors.New(fatalReason)
		}
		if (backlog == 0 || allDeferred) && startupTimer == nil && !stopping && ctx.Err() == nil {
			startConsumers()
		}
		select {
		case <-startupDeadline:
			if len(ready) != len(targets) {
				stop("consumer_ready_timeout")
			}
		case <-rootDone:
			rootDone = nil
			stop("shutdown")
			deadline = time.After(5 * time.Second)
		case <-deadline:
			emit("shutdown_pending", "database_queue_retained")
			return errors.New("shutdown deadline; pending SQLite submissions retained")
		case <-retry:
			retry = nil
		case in := <-incoming:
			if stopping {
				continue
			}
			switch in.kind {
			case "ready":
				ready[in.consumer] = true
				st.update(func(s *statusSnapshot) {
					s.ReadyConsumers = len(ready)
					s.ConsumerReady = len(ready) == len(targets)
					if s.ConsumerReady {
						s.Connection = "ready"
					}
				})
				emit("consumer_ready", "")
			case "exit", "disconnected":
				stop(in.reason)
			case "message":
				observedAt := time.Now().UTC()
				st.update(func(s *statusSnapshot) { s.LastReceivedAt = observedAt.Format(time.RFC3339Nano) })
				if !ready[in.consumer] {
					st.update(func(s *statusSnapshot) { s.Rejected++; s.LastError = "event_before_ready"; s.LastErrorAt = stamp() })
					emit("input_rejected", "event_before_ready")
					continue
				}
				item, err := normalize(in.line, c, targets[in.consumer])
				switch {
				case errors.Is(err, errSelfDirect):
					st.update(func(s *statusSnapshot) { s.OwnDMFiltered++ })
					emit("input_filtered", err.Error())
					continue
				case errors.Is(err, errUnmatched):
					st.update(func(s *statusSnapshot) { s.Unmatched++ })
					emit("input_filtered", err.Error())
					continue
				case err != nil:
					st.update(func(s *statusSnapshot) { s.Rejected++; s.LastError = err.Error(); s.LastErrorAt = stamp() })
					emit("input_rejected", err.Error())
					continue
				}
				inserted, err := queue.enqueue(item, observedAt)
				if errors.Is(err, errDWSQueueConflict) {
					st.update(func(s *statusSnapshot) {
						s.Rejected++
						s.Admission = "failed"
						s.LastError = "conflict"
						s.LastErrorAt = stamp()
					})
					emit("input_rejected", errDWSQueueConflict.Error())
					stop(errDWSQueueConflict.Error())
					continue
				}
				if err != nil {
					return queueFailed("database_write_failed")
				}
				if !inserted {
					st.update(func(s *statusSnapshot) { s.Duplicates++ })
					emit("input_duplicate", "message_already_stored")
					continue
				}
				backlog++
				st.update(func(s *statusSnapshot) { s.Backlog = backlog; s.Admission = "pending" })
				decision, _ := (dwsQueueItem{observedAt: observedAt, submission: item}).classify(evidence)
				if decision != "outbound_echo" && decision != "held" {
					if err := trackFeedback(item, observedAt); err != nil {
						return err
					}
				}
				if backlog == c.Capacity {
					stop("buffer_full")
				}
			}
		case result := <-results:
			active = false
			if result.Code == "" {
				if err := feedback.recordAdmission(head.submission.nativeReply(), result.Receipt); err != nil {
					emit("feedback_state_failed", "record_admission")
					stop("feedback_state_failed")
					return errors.New("cannot record DWS feedback admission")
				}
				var disposition string
				if head.submission.ReleaseReason != "" {
					disposition = "released"
				}
				if err := queue.finish(head.sequence, disposition); err != nil {
					return queueFailed("database_write_failed")
				}
				backlog--
				backoff = time.Duration(c.RetryMillis) * time.Millisecond
				st.update(func(s *statusSnapshot) {
					s.Backlog = backlog
					s.Accepted++
					if result.Receipt.Duplicate || result.Receipt.Status == "duplicate" {
						s.Duplicates++
					}
					s.Admission = "accepted"
				})
				emit("core_accepted", "")
			} else {
				st.update(func(s *statusSnapshot) { s.Admission = "failed"; s.LastError = result.Code; s.LastErrorAt = stamp() })
				emit("core_admission_failed", result.Code)
				if result.Retry {
					retry = time.After(backoff)
					backoff *= 2
					if backoff > 30*time.Second {
						backoff = 30 * time.Second
					}
				} else {
					if err := queue.finish(head.sequence, result.Code); err != nil {
						return queueFailed("database_write_failed")
					}
					backlog--
					st.update(func(s *statusSnapshot) { s.Backlog = backlog; s.Rejected++; s.PermanentFailures++ })
					stop("core_rejected_" + result.Code)
				}
			}
		}
	}
}

func Status(ctx context.Context, endpoint string, output io.Writer) error {
	if err := trigger.ValidateStatusEndpoint(endpoint); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/status", http.NoBody)
	if err != nil {
		return errors.New("invalid status request")
	}
	res, err := trigger.HTTPClient(3 * time.Second).Do(req)
	if err != nil {
		return errors.New("status unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status HTTP %d", res.StatusCode)
	}
	var s statusSnapshot
	if err := json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&s); err != nil {
		return errors.New("invalid status response")
	}
	return json.NewEncoder(output).Encode(s)
}
