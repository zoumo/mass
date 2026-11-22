package runtime

import "path/filepath"

// EqualNativePath compares absolute paths after resolving filesystem aliases.
func EqualNativePath(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	cleanA, cleanB := filepath.Clean(a), filepath.Clean(b)
	if cleanA == cleanB {
		return true
	}
	resolvedA, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	resolvedB, err := filepath.EvalSymlinks(b)
	return err == nil && filepath.Clean(resolvedA) == filepath.Clean(resolvedB)
}
