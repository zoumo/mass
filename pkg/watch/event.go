package watch

// Event is the standard watch event envelope.
// Seq is the stream-level cursor assigned by the publisher.
// Payload is the business content; the framework does not inspect it.
type Event[T any] struct {
	Seq     int `json:"seq"`
	Payload T   `json:"payload"`
}
