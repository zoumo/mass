// Package recovery stores adapter-private material. It contains no public
// resources or events and is not passed through Service or Store DTOs.
package recovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type Material struct {
	Format     int                 `json:"format"`
	Kind       string              `json:"kind"`
	Owner      string              `json:"owner"`
	ID         string              `json:"id"`
	Executable string              `json:"executable"`
	Address    runtime.Address     `json:"address"`
	Spec       runtime.SessionSpec `json:"spec"`
	Context    json.RawMessage     `json:"context"`
}
type Vault struct{ Dir, Kind, Owner, Executable string }

func digest(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func (v Vault) path(id string) (string, error) {
	if !filepath.IsAbs(v.Dir) || v.Owner == "" {
		return "", errors.New("absolute private recovery directory and owner required")
	}
	return filepath.Join(v.Dir, v.Kind, digest(v.Owner), digest(id)+".json"), nil
}

// Check validates configuration without reading or changing persistent material.
func (v Vault) Check() error { _, err := v.path("check"); return err }
func (v Vault) Save(id string, a runtime.Address, spec runtime.SessionSpec, native any) error {
	p, err := v.path(id)
	if err != nil {
		return err
	}
	context, err := json.Marshal(native)
	if err != nil {
		return err
	}
	b, err := json.Marshal(Material{Format: 1, Kind: v.Kind, Owner: v.Owner, ID: id, Executable: v.Executable, Address: a, Spec: spec, Context: context})
	if err != nil {
		return err
	}
	if previous, e := os.ReadFile(p); e == nil {
		if bytes.Equal(previous, b) {
			return nil
		}
		return errors.New("existing recovery material conflicts")
	} else if !os.IsNotExist(e) {
		return e
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".material-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), p); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
func (v Vault) Load(id string, a runtime.Address, spec runtime.SessionSpec) (Material, error) {
	p, err := v.path(id)
	if err != nil {
		return Material{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Material{}, err
	}
	var m Material
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, err
	}
	if d.Decode(new(any)) != io.EOF {
		return m, errors.New("trailing recovery data")
	}
	if m.Format != 1 || m.Kind != v.Kind || m.Owner != v.Owner || m.ID != id || m.Executable != v.Executable || m.Address != a || !reflect.DeepEqual(m.Spec, spec) || len(m.Context) == 0 {
		return m, errors.New("private recovery context mismatch")
	}
	return m, nil
}
