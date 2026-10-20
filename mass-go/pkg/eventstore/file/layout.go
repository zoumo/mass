package file

import (
	"os"

	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
)

func (s *Store) layoutLock(write bool) (*os.File, error) {
	return fileio.Lock(s.dir, "events.snapshot.lock", write)
}
