package watchdog

import "golang.org/x/sys/unix"

// ReapOrphans makes this process the parent of orphaned descendants it owns.
func ReapOrphans() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}
