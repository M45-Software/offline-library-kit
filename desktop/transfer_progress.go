package main

import (
	"math"
	"sync"
	"time"
)

const transferWindow = 10 * time.Second
const transferSampleInterval = 250 * time.Millisecond

type transferSample struct {
	at   time.Time
	done int64
}

// A meter belongs to one HTTP transfer attempt. Its baseline includes any
// resumed bytes, so partial verification and previous attempts never add speed.
type transferMeter struct {
	mu      sync.Mutex
	start   time.Time
	samples []transferSample
	latest  transferSample
}

func newTransferMeter(now time.Time, offset int64) *transferMeter {
	sample := transferSample{at: now, done: offset}
	return &transferMeter{start: now, samples: []transferSample{sample}, latest: sample}
}

func (m *transferMeter) record(now time.Time, done int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now.Before(m.latest.at) || done < m.latest.done {
		return
	}
	m.latest = transferSample{at: now, done: done}
	if now.Sub(m.samples[len(m.samples)-1].at) >= transferSampleInterval {
		m.samples = append(m.samples, m.latest)
	}
	cutoff := now.Add(-transferWindow)
	// Keep one sample before the window for interpolation at its boundary.
	first := 0
	for first+1 < len(m.samples) && !m.samples[first+1].at.After(cutoff) {
		first++
	}
	if first > 0 {
		m.samples = append([]transferSample{}, m.samples[first:]...)
	}
}

func (m *transferMeter) snapshot(now time.Time, total int64) (float64, *int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now.Before(m.latest.at) {
		now = m.latest.at
	}
	if now.Sub(m.start) < time.Second {
		return 0, nil, false
	}
	if now.Sub(m.latest.at) >= transferWindow {
		return 0, nil, true
	}
	cutoff := now.Add(-transferWindow)
	if cutoff.Before(m.start) {
		cutoff = m.start
	}
	left := m.samples[0]
	baseline := float64(left.done)
	for i := 1; i <= len(m.samples); i++ {
		right := m.latest
		if i < len(m.samples) {
			right = m.samples[i]
		}
		if !right.at.Before(cutoff) {
			if right.at.After(left.at) && cutoff.After(left.at) {
				fraction := cutoff.Sub(left.at).Seconds() / right.at.Sub(left.at).Seconds()
				baseline = float64(left.done) + float64(right.done-left.done)*fraction
			} else {
				baseline = float64(left.done)
			}
			break
		}
		left = right
		baseline = float64(left.done)
	}
	rate := math.Max(0, (float64(m.latest.done)-baseline)/now.Sub(cutoff).Seconds())
	if rate <= 0 || total <= m.latest.done {
		return rate, nil, true
	}
	seconds := math.Ceil(float64(total-m.latest.done) / rate)
	if seconds >= float64(math.MaxInt64) {
		return rate, nil, true
	}
	eta := int64(seconds)
	return rate, &eta, true
}

func (j job) withTransferProgress(now time.Time) job {
	j.BytesPerSecond, j.ETASeconds, j.SpeedMeasured = 0, nil, false
	if j.State == "downloading" && j.transfer != nil {
		j.BytesPerSecond, j.ETASeconds, j.SpeedMeasured = j.transfer.snapshot(now, j.Total)
	}
	return j
}
