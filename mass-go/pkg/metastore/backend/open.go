// Package backend composes the configured metadata authority.
package backend

import (
	"errors"

	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/metastore/internal/metadata"
)

type Store interface {
	metastore.MetaStore
	Close() error
}

func Open(dir string, config metastore.Config, readOnly bool) (Store, error) {
	switch config.Backend {
	case "", "sqlite":
		return metadata.OpenSQLite(dir, readOnly)
	case "file":
		return metadata.OpenFile(dir, readOnly)
	default:
		return nil, errors.New("unknown MetaStore backend: select sqlite or file")
	}
}
