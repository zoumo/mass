//go:build darwin

package runtimespec

// maxUnixSocketPath is the kernel limit for AF_UNIX sockaddr_un.sun_path on macOS.
const maxUnixSocketPath = 104
