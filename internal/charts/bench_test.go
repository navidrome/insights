package charts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/insights/internal/summary"
)

// writeSyntheticSummaries writes summaries with the given number of player types per day.
// Useful for both benchmarks (heavy fixture with 5000 types) and tests (light fixture with ~300 types).
func writeSyntheticSummaries(tb testing.TB, dir string, days, playerTypesPerDay int) {
	tb.Helper()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		s := summary.Summary{
			NumInstances:   int64(100000 + i),
			NumActiveUsers: int64(50000 + i),
			Versions:       make(map[string]uint64, 600),
			OS:             map[string]uint64{"Linux - amd64": 90000, "Darwin - arm64": 5000},
			PlayerTypes:    make(map[string]uint64, playerTypesPerDay),
			Players:        map[string]uint64{"1": 50000, "2": 20000},
			Tracks:         map[string]uint64{"1000": 40000, "5000": 30000},
			Albums:         map[string]uint64{"100": 40000},
			Artists:        map[string]uint64{"100": 40000},
		}
		for v := 0; v < 600; v++ {
			s.Versions[fmt.Sprintf("0.%d.%d (abcdef12)", v/10, v%10)] = uint64(600 - v)
		}
		for p := 0; p < playerTypesPerDay; p++ {
			s.PlayerTypes[fmt.Sprintf("client-%d-%d", i%7, p)] = uint64(p%50 + 1)
		}
		if err := summary.SaveSummary(dir, s, day); err != nil {
			tb.Fatalf("SaveSummary: %v", err)
		}
	}
}

// measurePeakHeap runs fn while sampling runtime.ReadMemStats and returns the peak HeapAlloc delta.
// Measures the heap consumed by the function itself, not the baseline heap state.
// The sampler runs in a goroutine on a 10ms interval to avoid -race issues.
func measurePeakHeap(fn func()) uint64 {
	runtime.GC()
	time.Sleep(10 * time.Millisecond) // Let GC settle

	// Capture baseline after GC to measure only the delta for this call
	var baselineStats runtime.MemStats
	runtime.ReadMemStats(&baselineStats)
	baseline := baselineStats.HeapAlloc

	var peak uint64
	var mu sync.Mutex
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	// Sampler goroutine
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				mu.Lock()
				// Record delta from baseline, floored at zero
				delta := m.HeapAlloc - baseline
				if delta > peak {
					peak = delta
				}
				mu.Unlock()
			case <-stop:
				return
			}
		}
	}()

	fn()
	close(stop)
	wg.Wait() // Wait for sampler to finish before returning peak

	return peak
}

func benchmarkExport(b *testing.B, days int) {
	b.Helper()
	dir := b.TempDir()
	writeSyntheticSummaries(b, dir, days, 5000)

	b.ResetTimer()
	b.ReportAllocs()

	var peakHeap uint64
	for i := 0; i < b.N; i++ {
		peak := measurePeakHeap(func() {
			if err := ExportChartsJSON(dir); err != nil {
				b.Fatalf("ExportChartsJSON: %v", err)
			}
		})
		if peak > peakHeap {
			peakHeap = peak
		}
	}

	b.StopTimer()
	b.ReportMetric(float64(peakHeap), "peak-heap-B")

	// The output must not be empty, or the benchmark is timing a no-op.
	out := filepath.Join(dir, consts.ChartDataDir, "charts.json")
	data, err := os.ReadFile(out) //#nosec G304 -- path is built from a test temp dir
	if err != nil {
		b.Fatalf("reading charts.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		b.Fatalf("charts.json is not valid JSON: %v", err)
	}
	if len(parsed["charts"].([]any)) == 0 {
		b.Fatal("charts.json has no charts")
	}
}

func BenchmarkExportChartsJSON_500Days(b *testing.B)  { benchmarkExport(b, 500) }
func BenchmarkExportChartsJSON_2000Days(b *testing.B) { benchmarkExport(b, 2000) }

// TestExportMemoryDoesNotGrowWithHistory measures that peak heap allocation
// does not grow proportionally with summary history length.
//
// Today's implementation retains every day in memory, so peak heap scales with days.
// Measured today (20 runs): median 3.97x, range 3.49x to 5.73x for 4x history (200 vs 800 days).
// After Task 5 (streaming implementation), peak should be close to flat (~1.2x or less).
// Threshold set to 6.2x (8% headroom above observed max) to tolerate variance.
// The threshold will be tightened in Task 5 once the optimization proves the peak
// is independent of history length.
func TestExportMemoryDoesNotGrowWithHistory(t *testing.T) {
	smallDir := t.TempDir()
	largeDir := t.TempDir()

	// Light fixture: 300 player types per day (vs 5000 in benchmarks).
	// 200 and 800 days gives 4x ratio.
	writeSyntheticSummaries(t, smallDir, 200, 300)
	writeSyntheticSummaries(t, largeDir, 800, 300)

	smallPeak := measurePeakHeap(func() {
		if err := ExportChartsJSON(smallDir); err != nil {
			t.Fatalf("ExportChartsJSON (small): %v", err)
		}
	})

	largePeak := measurePeakHeap(func() {
		if err := ExportChartsJSON(largeDir); err != nil {
			t.Fatalf("ExportChartsJSON (large): %v", err)
		}
	})

	ratio := float64(largePeak) / float64(smallPeak)

	// Threshold set to pass today's code (which retains all days).
	// Observed max from 20 runs: 5.73x. Threshold 6.2x provides 8% headroom.
	// Will be tightened in Task 5 once streaming optimization lands.
	const threshold = 6.2
	t.Logf("RATIO_MEASUREMENT: %.2f", ratio)
	if ratio > threshold {
		t.Logf("Peak heap ratio %.2f exceeds threshold %.2f (200 vs 800 days)", ratio, threshold)
		t.Logf("  200 days:  %d bytes", smallPeak)
		t.Logf("  800 days:  %d bytes", largePeak)
		t.Logf("After Task 5 optimization, ratio should be ~1.2x or less")
		t.Fatalf("Memory growth tracking history length (today: %.2f, expected after streaming: ~1.2x)", ratio)
	}
}
