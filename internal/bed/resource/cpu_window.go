package resource

import "time"

const cpuUsageWindowDuration = 10 * time.Second

type cpuUsageSample struct {
	at    time.Time
	usage time.Duration
}

// cpuUsageWindow uses cumulative CPU time, weighted by elapsed time rather than
// sample count. All carrier CPU reporting and admission share this one window.
// Its owner serializes updates; reads use the published AdmissionReport.
type cpuUsageWindow struct {
	samples []cpuUsageSample
	limit   float64
}

func (w *cpuUsageWindow) add(now time.Time, usage time.Duration, limit float64) (float64, bool) {
	if limit <= 0 {
		*w = cpuUsageWindow{}
		return 0, false
	}
	if n := len(w.samples); n > 0 {
		last := w.samples[n-1]
		// Counter resets and quota changes start a new comparable history.
		if usage < last.usage || !now.After(last.at) || limit != w.limit {
			w.samples = nil
		}
	}
	w.limit = limit
	w.samples = append(w.samples, cpuUsageSample{now, usage})
	cutoff := now.Add(-cpuUsageWindowDuration)
	for len(w.samples) > 1 && !w.samples[1].at.After(cutoff) {
		w.samples = w.samples[1:]
	}
	if len(w.samples) < 2 {
		return 0, false
	}
	first := w.samples[0]
	start, baseline := first.at, float64(first.usage)
	if start.Before(cutoff) {
		// Interpolate the boundary interval so delayed/irregular ticks do not
		// extend the window or give older CPU bursts extra weight.
		next := w.samples[1]
		baseline += float64(next.usage-first.usage) * float64(cutoff.Sub(start)) / float64(next.at.Sub(start))
		start = cutoff
	}
	// During startup, use the observed duration, never pad history with zeroes.
	return (float64(usage) - baseline) / float64(now.Sub(start)) / limit * 100, true
}
