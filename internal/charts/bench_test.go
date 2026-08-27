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

// BenchmarkExportChartsJSON_* benchmarks measure cumulative allocation and sampled peak
// heap during ExportChartsJSON. These are for informational comparison before/after
// streaming optimization, NOT for gating. The gate is TestLoadChartInputRetainedHeap
// which uses a deterministic bracketed-GC method to measure actual retained memory.
func BenchmarkExportChartsJSON_500Days(b *testing.B)  { benchmarkExport(b, 500) }
func BenchmarkExportChartsJSON_2000Days(b *testing.B) { benchmarkExport(b, 2000) }

// TestLoadChartInputRetainedHeap measures that heap retained by loadChartInput
// does not grow proportionally with summary history length.
//
// Uses bracketed-GC method: GC before load, load, GC after, measure retained delta.
// This measures deterministically what loadChartInput keeps on the heap after GC sweeps
// away ephemeral allocations. Sampled peak heap was abandoned because it showed 64%
// spread (3.49x to 5.73x) which made gate thresholds either too loose or too tight.
//
// Today's implementation retains a slim record per day in the Series slice, so heap
// scales with history length. Measured today (20 runs): median 3.93x, range 3.89x to
// 4.60x for 4x history (200 vs 800 days). Note: 71% spread remains; this is likely
// inherent to loadChartInput's heap allocation pattern, not measurement noise.
// After Task 5 (streaming implementation), ratio should be close to flat (~1.1x or less).
// Threshold set to catch regressions without false positives from natural variance.
func TestLoadChartInputRetainedHeap(t *testing.T) {
	smallDir := t.TempDir()
	largeDir := t.TempDir()

	// Light fixture: 300 player types per day.
	// 200 and 800 days gives 4x day count.
	writeSyntheticSummaries(t, smallDir, 200, 300)
	writeSyntheticSummaries(t, largeDir, 800, 300)

	// Measure retained heap for 200 days using bracketed-GC method
	runtime.GC()
	var beforeSmall runtime.MemStats
	runtime.ReadMemStats(&beforeSmall)

	smallInput, err := loadChartInput(smallDir)
	if err != nil {
		t.Fatalf("loadChartInput (small): %v", err)
	}

	runtime.GC()
	var afterSmall runtime.MemStats
	runtime.ReadMemStats(&afterSmall)
	runtime.KeepAlive(smallInput) // Guarantee smallInput is still live at the reading above

	retainedSmall := afterSmall.HeapAlloc - beforeSmall.HeapAlloc

	// Measure retained heap for 800 days using bracketed-GC method
	runtime.GC()
	var beforeLarge runtime.MemStats
	runtime.ReadMemStats(&beforeLarge)

	largeInput, err := loadChartInput(largeDir)
	if err != nil {
		t.Fatalf("loadChartInput (large): %v", err)
	}

	runtime.GC()
	var afterLarge runtime.MemStats
	runtime.ReadMemStats(&afterLarge)
	runtime.KeepAlive(largeInput) // Guarantee largeInput is still live at the reading above

	retainedLarge := afterLarge.HeapAlloc - beforeLarge.HeapAlloc

	ratio := float64(retainedLarge) / float64(retainedSmall)

	// Threshold set to catch regressions. Measured today (20 runs):
	// median 3.93x, range 3.89x to 4.60x for 4x history (200 vs 800 days).
	// Set threshold to 5.1x (11% headroom above observed max) to tolerate
	// variance while catching regressions like reintroducing half the per-day retention.
	const threshold = 5.1
	t.Logf("RATIO_MEASUREMENT: %.2f", ratio)
	if ratio > threshold {
		t.Logf("Retained heap ratio %.2f exceeds threshold %.2f (200 vs 800 days)", ratio, threshold)
		t.Logf("  200 days:  %d bytes", retainedSmall)
		t.Logf("  800 days:  %d bytes", retainedLarge)
		t.Logf("After Task 5 optimization, ratio should be ~1.1x or less")
		t.Fatalf("Memory retained by loadChartInput grows with history length (today: %.2f, expected after streaming: ~1.1x)", ratio)
	}
}
