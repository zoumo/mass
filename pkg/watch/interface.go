package watch

// Interface is the watch interface, modeled after k8s.io/apimachinery/pkg/watch.Interface.
//
// Usage:
//
//	w, err := client.WatchEvent(ctx, fromSeq)
//	if err != nil { ... }
//	defer w.Stop()
//	for ev := range w.ResultChan() { ... }
//
// Stop must be idempotent and safe to call concurrently.
type Interface[T any] interface {
	// Stop terminates the watch stream and closes ResultChan.
	// Idempotent — safe to call multiple times from any goroutine.
	Stop()

	// ResultChan returns the channel delivering events.
	// Closed when Stop is called or the underlying stream ends.
	ResultChan() <-chan T
}
