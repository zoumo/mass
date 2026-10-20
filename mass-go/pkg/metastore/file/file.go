// Package file provides file-authoritative resource persistence without SQL.
package file

import "github.com/zoumo/mass/mass-go/pkg/metastore/internal/metadata"

type Store = metadata.Store

func Open(dir string, readOnly bool) (*Store, error) { return metadata.OpenFile(dir, readOnly) }
