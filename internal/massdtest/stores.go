// Package massdtest opens real persistence backends for application tests.
package massdtest

import (
	"errors"

	eventfile "github.com/zoumo/mass/massd/pkg/eventstore/file"
	"github.com/zoumo/mass/massd/pkg/metastore"
	"github.com/zoumo/mass/massd/pkg/metastore/backend"
)

type FileStores struct {
	Meta   backend.Store
	Events *eventfile.Store
}

func OpenFileStores(dir string, readOnly bool) (*FileStores, error) {
	meta, err := backend.Open(dir, metastore.Config{Backend: "file"}, readOnly)
	if err != nil {
		return nil, err
	}
	events, err := eventfile.Open(dir, readOnly)
	if err != nil {
		return nil, errors.Join(err, meta.Close())
	}
	return &FileStores{Meta: meta, Events: events}, nil
}

func (s *FileStores) Close() error {
	return errors.Join(s.Events.Close(), s.Meta.Close())
}
