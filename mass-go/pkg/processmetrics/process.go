// Package processmetrics instruments the OS process and Go runtime of a service.
package processmetrics

import (
	"context"
	"log/slog"
	"math"
	"os"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/common"
	"github.com/shirou/gopsutil/v4/process"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/semconv/v1.43.0/goconv"
	"go.opentelemetry.io/otel/semconv/v1.43.0/processconv"
	"golang.org/x/sys/unix"
)

const scopeName = "github.com/zoumo/mass/mass-go/pkg/processmetrics"

// Register attaches process observations and Go runtime instruments to the
// service provider. Also pass WithRuntimeHistograms to the service reader.
func Register(provider metric.MeterProvider, logger *slog.Logger) {
	missing := &sampleDiagnostics{logger: logger}
	if err := registerProcess(provider.Meter(scopeName), readProcess, missing); err != nil {
		logger.Warn("OpenTelemetry process metric registration failed")
	}
	if err := otelruntime.Start(otelruntime.WithMeterProvider(provider)); err != nil {
		logger.Warn("OpenTelemetry Go runtime metric registration failed")
	}
}

type sampleDiagnostics struct {
	logger   *slog.Logger
	reported sync.Map
}

func (d *sampleDiagnostics) warn(name string) {
	if _, reported := d.reported.LoadOrStore(name, struct{}{}); !reported {
		d.logger.Warn("OpenTelemetry metric sample unavailable", "metric", name)
	}
}

type processSample struct {
	userCPU, systemCPU float64
	cpuOK              bool
	rss                int64
	rssOK              bool
	fds                int64
	fdsOK              bool
	fdLimit            uint64
	fdLimitOK          bool
	uptime             float64
	uptimeOK           bool
}

func readProcess(ctx context.Context) processSample {
	var sample processSample
	// A host-monitoring override may refer to a different PID namespace.
	ctx = context.WithValue(ctx, common.EnvKey, common.EnvMap{common.HostProcEnvKey: "/proc"})
	p := &process.Process{Pid: int32(os.Getpid())}

	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err == nil {
		sample.userCPU = time.Duration(usage.Utime.Nano()).Seconds()
		sample.systemCPU = time.Duration(usage.Stime.Nano()).Seconds()
		sample.cpuOK = true
	}
	if info, err := p.MemoryInfoWithContext(ctx); err == nil && info.RSS > 0 && info.RSS <= math.MaxInt64 {
		sample.rss, sample.rssOK = int64(info.RSS), true
	}
	if count, err := countOpenFDs(ctx, p); err == nil {
		sample.fds, sample.fdsOK = count, true
	}
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err == nil {
		sample.fdLimit, sample.fdLimitOK = limit.Cur, true
	}
	if started, err := p.CreateTimeWithContext(ctx); err == nil && started > 0 {
		sample.uptime, sample.uptimeOK = max(0, time.Since(time.UnixMilli(started)).Seconds()), true
	}
	return sample
}

func registerProcess(meter metric.Meter, sample func(context.Context) processSample, missing *sampleDiagnostics) error {
	cpu, err := meter.Float64ObservableCounter("process.cpu.time",
		metric.WithUnit("s"), metric.WithDescription("Total CPU seconds broken down by CPU mode."))
	if err != nil {
		return err
	}
	rss, err := processconv.NewMemoryUsageObservable(meter)
	if err != nil {
		return err
	}
	fds, err := processconv.NewUnixFileDescriptorCountObservable(meter)
	if err != nil {
		return err
	}
	const limitName = "mass.process.unix.file_descriptor.limit"
	limit, err := meter.Int64ObservableGauge(limitName,
		metric.WithUnit(fds.Unit()), metric.WithDescription("Process soft limit for open file descriptors."))
	if err != nil {
		return err
	}
	uptime, err := processconv.NewUptimeObservable(meter)
	if err != nil {
		return err
	}
	cycles, err := goconv.NewMemoryGCCyclesObservable(meter)
	if err != nil {
		return err
	}

	userCPU := metric.WithAttributeSet(attribute.NewSet(attribute.String("cpu.mode", "user")))
	systemCPU := metric.WithAttributeSet(attribute.NewSet(attribute.String("cpu.mode", "system")))
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		values := sample(ctx)
		if values.cpuOK {
			observer.ObserveFloat64(cpu, values.userCPU, userCPU)
			observer.ObserveFloat64(cpu, values.systemCPU, systemCPU)
		} else {
			missing.warn("process.cpu.time")
		}
		if values.rssOK {
			observer.ObserveInt64(rss.Inst(), values.rss)
		} else {
			missing.warn(rss.Name())
		}
		if values.fdsOK {
			observer.ObserveInt64(fds.Inst(), values.fds)
		} else {
			missing.warn(fds.Name())
		}
		if !values.fdLimitOK {
			missing.warn(limitName)
		} else if values.fdLimit != unix.RLIM_INFINITY && values.fdLimit <= math.MaxInt64 {
			observer.ObserveInt64(limit, int64(values.fdLimit))
		}
		if values.uptimeOK {
			observer.ObserveFloat64(uptime.Inst(), values.uptime)
		} else {
			missing.warn(uptime.Name())
		}
		gc := [1]metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}}
		metrics.Read(gc[:])
		if value := gc[0].Value; value.Kind() == metrics.KindUint64 {
			observer.ObserveInt64(cycles.Inst(), int64(min(value.Uint64(), math.MaxInt64)))
		} else {
			missing.warn(cycles.Name())
		}
		return nil
	}, cpu, rss.Inst(), fds.Inst(), limit, uptime.Inst(), cycles.Inst())
	return err
}
