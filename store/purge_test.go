package store

import (
	"os"
	"path/filepath"
	"time"

	"github.com/navidrome/insights/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PurgeOldFiles", func() {
	var dir string
	var today time.Time

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		today = time.Now().UTC().Truncate(24 * time.Hour)
	})

	It("succeeds when the reports directory does not exist", func() {
		Expect(PurgeOldFiles(dir, 15)).To(Succeed())
	})

	It("keeps files inside the retention window", func() {
		writeDay(dir, today, "a")
		writeDay(dir, today.AddDate(0, 0, -14), "b")

		Expect(PurgeOldFiles(dir, 15)).To(Succeed())

		Expect(HasDay(dir, today)).To(BeTrue())
		Expect(HasDay(dir, today.AddDate(0, 0, -14))).To(BeTrue())
	})

	It("deletes files older than the retention window", func() {
		old := today.AddDate(0, 0, -20)
		writeDay(dir, old, "b")
		writeDay(dir, today, "a")

		Expect(PurgeOldFiles(dir, 15)).To(Succeed())

		Expect(HasDay(dir, old)).To(BeFalse())
		Expect(HasDay(dir, today)).To(BeTrue())
	})

	// The two specs above only pin the cutoff to somewhere between 14 and 20 days back. This
	// one pins it exactly: retentionDays days back is the oldest day kept, and the day before
	// it goes. Either direction of an off-by-one fails here.
	It("keeps the day at the cutoff and deletes the day before it", func() {
		keep := today.AddDate(0, 0, -15)
		drop := today.AddDate(0, 0, -16)
		writeDay(dir, keep, "a")
		writeDay(dir, drop, "b")

		Expect(PurgeOldFiles(dir, 15)).To(Succeed())

		Expect(HasDay(dir, keep)).To(BeTrue())
		Expect(HasDay(dir, drop)).To(BeFalse())
	})

	// A day is stored as one segment per writer session, and a segment may have been
	// decompressed by hand. Every one of them expires with the day: the day directory is left
	// empty, so its removal proves nothing was missed.
	It("deletes every segment of an expired day, compressed or not", func() {
		old := today.AddDate(0, 0, -20)
		writeDay(dir, old, "a")
		writeDay(dir, old, "b")
		plain := filepath.Join(dayDir(dir, old), segmentPrefix(old)+"003"+plainReportFileExt)
		Expect(os.WriteFile(plain, []byte("{}\n"), consts.FilePermissions)).To(Succeed())
		Expect(DaySegmentPaths(dir, old)).To(HaveLen(3))

		Expect(PurgeOldFiles(dir, 15)).To(Succeed())

		Expect(DaySegmentPaths(dir, old)).To(BeEmpty())
		_, err := os.Stat(dayDir(dir, old))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("removes directories left empty by the purge", func() {
		old := today.AddDate(0, 0, -400) // a different year
		writeDay(dir, old, "b")

		monthDir := filepath.Dir(DaySegmentPaths(dir, old)[0])
		yearDir := filepath.Dir(monthDir)

		Expect(PurgeOldFiles(dir, 15)).To(Succeed())

		_, err := os.Stat(monthDir)
		Expect(os.IsNotExist(err)).To(BeTrue())
		_, err = os.Stat(yearDir)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	// The reports root is where ingest creates its lock file and today's day directory, so
	// pruning must stop above it. The lock file is removed here on purpose: while it exists
	// the root is never empty, and this spec would hold no matter what pruning did.
	It("keeps the reports root even when the purge leaves it empty", func() {
		old := today.AddDate(0, 0, -400)
		writeDay(dir, old, "b")
		reportsDir := filepath.Join(dir, consts.ReportsDir)
		Expect(os.Remove(filepath.Join(reportsDir, lockFileName))).To(Succeed())

		Expect(PurgeOldFiles(dir, 15)).To(Succeed())

		entries, err := os.ReadDir(reportsDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})

	It("leaves the writer lock file alone", func() {
		writeDay(dir, today.AddDate(0, 0, -20), "b")

		Expect(PurgeOldFiles(dir, 15)).To(Succeed())

		_, err := os.Stat(filepath.Join(dir, consts.ReportsDir, lockFileName))
		Expect(err).ToNot(HaveOccurred())
	})
})
