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
				// Record delta from baseline, floored at zero: HeapAlloc can dip below the
				// baseline between GC cycles, and an unsigned subtraction without the floor
				// would wrap to roughly 2^64 instead of reporting no growth.
				var delta uint64
				if m.HeapAlloc > baseline {
					delta = m.HeapAlloc - baseline
				}
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

// The two benchmarks below are the before-and-after story for a human to read, not the gate.
// They report B/op (cumulative allocation, which the streaming rewrite made larger: three
// discarding passes churn more bytes in total than one retaining pass) alongside peak-heap-B
// (sampled, so it moves with wherever the collector happened to be when it looked). Neither
// number is asserted on. TestLoadChartInputRetainedHeapPerDay is the only thing that fails on a
// regression.
func BenchmarkExportChartsJSON_500Days(b *testing.B)  { benchmarkExport(b, 500) }
func BenchmarkExportChartsJSON_2000Days(b *testing.B) { benchmarkExport(b, 2000) }

// retainedHeapBytesPerDay loads dir and returns the heap the loaded result keeps alive, divided
// by the number of days it kept.
//
// The load is bracketed by two collections: GC, read HeapAlloc, load, GC, read HeapAlloc, with
// the result kept alive across the second reading. The second collection sweeps everything the
// passes allocated and then dropped, so the difference is what the result retains rather than
// what the load churned through. Cumulative allocation (B/op) cannot answer this question: it
// counts the churn, and the churn went up when retention went down.
func retainedHeapBytesPerDay(t *testing.T, dir string) float64 {
	t.Helper()

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	in, err := loadChartInput(dir)
	if err != nil {
		t.Fatalf("loadChartInput: %v", err)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(in) // the second reading must see in as reachable, or it measures nothing

	if len(in.Series) == 0 {
		t.Fatal("loadChartInput returned no days: the measurement would be of a no-op")
	}
	if after.HeapAlloc <= before.HeapAlloc {
		t.Fatalf("retained heap did not grow across the load: before %d B, after %d B",
			before.HeapAlloc, after.HeapAlloc)
	}
	return float64(after.HeapAlloc-before.HeapAlloc) / float64(len(in.Series))
}

// retainedBudgetPerDay is what one day of history is allowed to cost, in bytes of live heap once
// the chart input is loaded.
//
// A per-day figure, not a ratio between two history lengths. Retained heap is linear in the
// number of days both before and after the streaming rewrite, because loadChartInput keeps one
// record per day either way, so a 4x span gives a ratio near 4 whichever implementation is
// running and the ratio distinguishes nothing. An earlier version of this test asserted exactly
// that and was measuring the slope, which never changed. What changed is the constant: the old
// code held the whole Summary for every day, the new code holds a daySeries. Over 564 real
// production summaries that is 62.8 MB before against 1.00 MB after, or about 111 KB per day
// against about 1.8 KB per day.
//
// Measured on the fixture below, 20 runs under -race plus 10 without: min 1260 B, max 1395 B,
// median 1332 B per day, a 10% spread end to end. The budget is 8 KB, six times the worst
// reading and still fourteen times under the 111 KB per day the old code cost, so it fails long
// before a regression gets anywhere near reinstating per-day retention, and it is nowhere near
// tight enough for measurement noise to reach. Deliberately loose: there are two orders of
// magnitude between the two implementations and no reason to sit close to either edge.
const retainedBudgetPerDay = 8 * 1024

func TestLoadChartInputRetainedHeapPerDay(t *testing.T) {
	// 600 days is close to the production history these numbers came from, and long enough that
	// the fixed part of chartInput (the one full Latest summary, the version names) does not
	// distort the per-day figure: comparing this size against 2400 days puts that fixed part at
	// about 23 KB, so it contributes roughly 3% of the reading here. Longer only buys a slower
	// test, and writing the fixture is most of the runtime.
	const days = 600
	dir := t.TempDir()
	writeSyntheticSummaries(t, dir, days, 300)

	perDay := retainedHeapBytesPerDay(t, dir)
	t.Logf("retained heap: %.0f B per day over %d days, budget %d B", perDay, days, retainedBudgetPerDay)

	if perDay > retainedBudgetPerDay {
		t.Fatalf("loadChartInput retains %.0f B per day of history, over the %d B budget: "+
			"the passes are meant to keep one slim record per day, so something is holding a "+
			"per-day payload again", perDay, retainedBudgetPerDay)
	}
}
