//go:build !linux

package watchdog

// Other supported platforms reparent orphans to the system reaper.
func ReapOrphans() error { return nil }
