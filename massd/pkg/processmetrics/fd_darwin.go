package processmetrics

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

func countOpenFDs(ctx context.Context, p *process.Process) (int64, error) {
	count, err := p.NumFDsWithContext(ctx)
	return int64(count), err
}
