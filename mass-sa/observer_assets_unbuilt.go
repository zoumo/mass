//go:build !observerweb

package sa

import (
	"errors"
	"io/fs"
)

func observerAssets() (fs.FS, error) {
	return nil, errors.New("observer frontend not built")
}
