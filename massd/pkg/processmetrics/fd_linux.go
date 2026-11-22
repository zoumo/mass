package processmetrics

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/shirou/gopsutil/v4/process"
)

func countOpenFDs(ctx context.Context, _ *process.Process) (int64, error) {
	directory, err := os.Open("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	defer directory.Close()

	// The directory read itself opens an FD in the measured process.
	samplerFD := strconv.FormatUint(uint64(directory.Fd()), 10)
	var count int64
	for ctx.Err() == nil {
		names, err := directory.Readdirnames(256)
		for _, name := range names {
			if name != samplerFD {
				count++
			}
		}
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
	}
	return 0, ctx.Err()
}
