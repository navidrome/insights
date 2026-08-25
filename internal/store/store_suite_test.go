package store

import (
	"compress/gzip"
	"os"
	"testing"
	"time"

	"github.com/navidrome/insights/internal/consts"
	"github.com/navidrome/navidrome/core/metrics/insights"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Store Suite")
}

// mustPaths is DaySegmentPaths for the specs that only care about the paths. A listing error
// is a broken fixture, so it fails the spec rather than being threaded through every call.
func mustPaths(dataFolder string, date time.Time) []string {
	GinkgoHelper()
	paths, err := DaySegmentPaths(dataFolder, date)
	Expect(err).ToNot(HaveOccurred())
	return paths
}

// testDay is the fixed UTC day used across the suite.
var testDay = time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

// dataFor builds a minimal payload with the given instance ID.
func dataFor(id string) insights.Data {
	var d insights.Data
	d.InsightsID = id
	d.Version = "0.61.2"
	d.Library.Tracks = 100
	return d
}

// appendPlainLine appends a raw line as its own gzip member, as a hand-edited tail looks.
func appendPlainLine(dataFolder string, date time.Time, line string) {
	GinkgoHelper()
	paths, _ := DaySegmentPaths(dataFolder, date)
	Expect(paths).ToNot(BeEmpty())
	f, err := os.OpenFile(paths[len(paths)-1], os.O_WRONLY|os.O_APPEND, consts.FilePermissions) //#nosec G304 -- test-only path from the suite's TempDir
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	_, err = gz.Write([]byte(line + "\n"))
	Expect(err).ToNot(HaveOccurred())
	Expect(gz.Close()).To(Succeed())
}
