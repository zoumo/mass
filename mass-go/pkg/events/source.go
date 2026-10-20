package events

// Source is execution-only association. It never enters event JSON/history.
// A run token is fixed at open; work is the original native turn identity.
type Source struct{ run, work string }

func NewSource() Source                      { return Source{run: UUID()} }
func (s Source) WithWork(work string) Source { return Source{run: s.run, work: work} }
func (s Source) SameRun(other Source) bool   { return s.run != "" && s.run == other.run }
func (s Source) Work() string                { return s.work }
