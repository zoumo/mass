package trigger

// Descriptor contains the configuration Compose uses to check resource bindings
// and isolate the child process environment. It exposes credential names only.
type Descriptor struct {
	Source, AccountID, InstanceID  string
	CoreTokenEnv, UpstreamTokenEnv string
	StatusListen, DatabasePath     string
	FeedbackPath                   string
	OutboxConsumer                 bool
}
