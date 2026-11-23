package dws

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// A Core account has a single local DWS owner, including when
// someone starts a second component with a different queue database path.
func lockDWSOwner(c dwsConfig) (func(), error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, errors.New("cannot locate the DWS state owner directory")
	}
	dir := filepath.Join(cache, "superagent-dws-owners")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("cannot create the DWS state owner directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("DWS state owner requires a private directory")
	}
	endpoint, err := url.Parse(strings.TrimRight(c.CoreEndpoint, "/"))
	if err != nil {
		return nil, errors.New("invalid DWS Core origin")
	}
	endpoint.Host = strings.ToLower(endpoint.Host)
	key := sha256.Sum256([]byte(endpoint.String() + "\x00" + c.Source + "\x00" + c.AccountID))
	lock, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%x.lock", key)), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, errors.New("cannot open the DWS state owner lock")
	}
	closeLock := func() { _ = lock.Close() }
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeLock()
		return nil, errors.New("another DWS process owns this account and Core endpoint")
	}
	return closeLock, nil
}
