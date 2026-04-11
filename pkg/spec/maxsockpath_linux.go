//go:build linux

package spec

// maxUnixSocketPath is the kernel limit for AF_UNIX sockaddr_un.sun_path on Linux.
const maxUnixSocketPath = 108
