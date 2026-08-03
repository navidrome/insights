package summary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SaveSummary", func() {
	var dir string
	var day time.Time

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		day = time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
		Expect(os.Setenv("DATA_FOLDER", dir)).To(Succeed())
		DeferCleanup(func() { _ = os.Unsetenv("DATA_FOLDER") })
	})

	// A summary big enough that writing it is not instantaneous, which is what a real one
	// looks like: thousands of version, player and suffix keys.
	bigSummary := func(n int) Summary {
		s := Summary{NumInstances: 1, Versions: make(map[string]uint64, n)}
		for i := range n {
			s.Versions[fmt.Sprintf("0.61.%06d", i)] = uint64(i) //#nosec G115 -- test-only, i is small and non-negative
		}
		return s
	}

	It("writes a summary that reads back", func() {
		Expect(SaveSummary(Summary{NumInstances: 7}, day)).To(Succeed())

		summaries, err := GetSummaries()
		Expect(err).ToNot(HaveOccurred())
		Expect(summaries).To(HaveLen(1))
		Expect(summaries[0].Data.NumInstances).To(Equal(int64(7)))
	})

	It("leaves no temporary file behind", func() {
		Expect(SaveSummary(Summary{NumInstances: 7}, day)).To(Succeed())

		entries, err := os.ReadDir(filepath.Dir(SummaryFilePath(day)))
		Expect(err).ToNot(HaveOccurred())
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		Expect(names).To(ConsistOf("summary-2026-08-03.json"))
	})

	// The reason the write has to be atomic. Summarization overlaps with chart generation and
	// with the /charts handler, both of which call GetSummaries. A plain O_TRUNC write leaves
	// the file empty for as long as the write takes, and a reader that lands in that window
	// logs "skipping malformed file" and silently drops the day from the charts.
	It("never exposes a half-written summary to a concurrent reader", func() {
		const keys = 20000
		summary := bigSummary(keys)
		Expect(SaveSummary(summary, day)).To(Succeed())
		path := SummaryFilePath(day)

		// stop keeps a failing spec from leaking a writer into the next one: SaveSummary
		// resolves DATA_FOLDER on every call, so a leaked writer would save into whatever
		// temp dir the following spec sets up.
		stop := make(chan struct{})
		done := make(chan struct{})
		DeferCleanup(func() {
			close(stop)
			<-done
		})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			for range 200 {
				select {
				case <-stop:
					return
				default:
				}
				Expect(SaveSummary(summary, day)).To(Succeed())
			}
		}()

		// Reads the file the way GetSummaries does, but without the directory walk, so the
		// loop is tight enough to land inside a save that is in progress.
		reads := 0
		for {
			select {
			case <-done:
				Expect(reads).To(BeNumerically(">", 0))
				return
			default:
			}
			data, err := os.ReadFile(path) //#nosec G304 -- test-only path from the suite's TempDir
			Expect(err).ToNot(HaveOccurred())
			var got Summary
			Expect(json.Unmarshal(data, &got)).To(Succeed(),
				"read %d of %d bytes was not a complete summary: a save in progress must not be visible", reads, len(data))
			Expect(got.Versions).To(HaveLen(keys))
			reads++
		}
	})
})
