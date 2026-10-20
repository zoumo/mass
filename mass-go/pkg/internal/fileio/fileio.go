// Package fileio provides stateless JSON and filesystem operations. It owns no
// storage lifecycle, transaction, resource catalog, recovery record or cache.
package fileio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

func SafeComponent(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "/\\\x00")
}

func CheckPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("unexpected storage entry: %s", path)
	}
	return nil
}

func SyncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func EnsureDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		if !os.IsExist(err) {
			return err
		}
		return CheckPath(path, true)
	}
	return SyncDirectory(filepath.Dir(path))
}

// AtomicFile durably replaces a JSON file, skipping identical bytes.
func AtomicFile(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return AtomicBytes(path, b)
}

func AtomicBytes(path string, b []byte) error {
	if err := CheckPath(path, false); err != nil && !os.IsNotExist(err) {
		return err
	}
	previous, err := os.ReadFile(path)
	if err == nil && bytes.Equal(previous, b) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return SyncDirectory(filepath.Dir(path))
}

func ReadJSON(path string, value any) error {
	if err := CheckPath(path, false); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return Decode(b, value)
}

func Decode(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing snapshot data")
	}
	return nil
}

func CopyValue[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = Decode(b, &out)
	}
	return out, err
}

func Equal(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && bytes.Equal(x, y)
}

func Lock(dir, name string, write bool) (*os.File, error) {
	flags, operation := os.O_RDONLY, syscall.LOCK_SH
	if write {
		flags, operation = os.O_CREATE|os.O_RDWR, syscall.LOCK_EX
	}
	path := filepath.Join(dir, name)
	if err := CheckPath(path, false); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), operation); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
