package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/navidrome/insights/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// baseFree is well clear of the report tree's size, so a target of baseFree + <some days> is
// met by exactly those days.
const baseFree = uint64(1) << 30

// unreachableTarget is more than any spec's tree can release, so the retention floor stops the
// purge.
const unreachableTarget = uint64(1) << 40

// fakeVolume re-measures the report tree on every probe, so free space rises by exactly what
// the purge frees. A constant probe would make "stops at the target" untestable.
type fakeVolume struct {
	dir         string
	freeAtStart uint64
	usedAtStart uint64
	probes      int
	err         error
	// failAfter is the probe number err starts at, counting from 1. Zero fails the first.
	failAfter int
}

func newFakeVolume(dir string, freeAtStart uint64) *fakeVolume {
	return &fakeVolume{dir: dir, freeAtStart: freeAtStart, usedAtStart: reportBytes(dir)}
}

func (v *fakeVolume) probe(string) (uint64, error) {
	v.probes++
	if v.err != nil && v.probes >= max(v.failAfter, 1) {
		return 0, v.err
	}
	return v.freeAtStart + (v.usedAtStart - reportBytes(v.dir)), nil
}

// install points the purge at this fake volume for the rest of the spec.
func (v *fakeVolume) install() *fakeVolume {
	GinkgoHelper()
	prev := freeBytes
	freeBytes = v.probe
	DeferCleanup(func() { freeBytes = prev })
	return v
}

// reportBytes is the size of the whole report tree, lock file included.
func reportBytes(dir string) uint64 {
	var total uint64
	_ = filepath.WalkDir(filepath.Join(dir, consts.ReportsDir), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a tree that is not there yet contributes nothing
		}
		if info, statErr := d.Info(); statErr == nil {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}

// dayBytes is what the volume gains when a day is purged. It matches on the file prefix, not
// DaySegmentPaths, so both forms of a segment count, exactly as the purge deletes them.
func dayBytes(dir string, date time.Time) uint64 {
	GinkgoHelper()
	entries, err := os.ReadDir(dayDir(dir, date))
	Expect(err).ToNot(HaveOccurred())
	var total uint64
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), segmentPrefix(date)) {
			continue
		}
		info, infoErr := e.Info()
		Expect(infoErr).ToNot(HaveOccurred())
		total += uint64(info.Size())
	}
	return total
}

// abandonedSegment writes what an earlier purge leaves behind when it hid a day and then
// failed to unlink one segment. No reader can see it, so only the purge can reclaim it.
func abandonedSegment(dir string, date time.Time, size int) string {
	GinkgoHelper()
	name := purgingPrefix + "reports-" + date.Format(consts.DateFormat) + ".007" + consts.ReportFileExt
	path := filepath.Join(dayDir(dir, date), name)
	Expect(os.MkdirAll(filepath.Dir(path), consts.DirPermissions)).To(Succeed())
	Expect(os.WriteFile(path, []byte(strings.Repeat("x", size)), consts.FilePermissions)).To(Succeed())
	return path
}

var _ = Describe("PurgeToFreeSpace", func() {
	var dir string
	var today time.Time

	// daysAgo returns the UTC day n days before today.
	daysAgo := func(n int) time.Time { return today.AddDate(0, 0, -n) }

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		today = time.Now().UTC().Truncate(24 * time.Hour)
	})

	// Statfs("") fails with ENOENT, so retention would never run. The real probe on purpose,
	// with a 1-byte target so the purge returns before touching anything.
	It("probes the current directory when the data folder is empty", func() {
		Expect(PurgeToFreeSpace("", 1, 7)).To(Succeed())
	})

	It("succeeds when the reports directory does not exist", func() {
		newFakeVolume(dir, 0).install()

		Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())
	})

	// Exactly at the target, which pins the comparison to >=.
	It("deletes nothing and logs nothing when free space already meets the target", func() {
		writeDay(dir, daysAgo(30), "a")
		writeDay(dir, daysAgo(20), "b")
		vol := newFakeVolume(dir, baseFree).install()

		out := captureLog(func() {
			Expect(PurgeToFreeSpace(dir, baseFree, 7)).To(Succeed())
		})

		Expect(out).To(BeEmpty())
		Expect(HasDay(dir, daysAgo(30))).To(BeTrue())
		Expect(HasDay(dir, daysAgo(20))).To(BeTrue())
		Expect(vol.probes).To(Equal(1), "one probe, after the sweep found nothing")
	})

	// A kill between two renames leaves a day part hidden and part visible. Repair is not
	// retention, so it must not wait for disk pressure, and it must take the whole day: leaving
	// the visible half is what a backfill would summarize as if it were the full day.
	It("finishes an interrupted purge even when there is no disk pressure", func() {
		writeDay(dir, daysAgo(30), "a")
		writeDay(dir, daysAgo(20), "b")
		orphan := abandonedSegment(dir, daysAgo(30), 4096)
		newFakeVolume(dir, baseFree).install()

		out := captureLog(func() {
			Expect(PurgeToFreeSpace(dir, baseFree, 7)).To(Succeed())
		})

		Expect(orphan).ToNot(BeAnExistingFile())
		Expect(out).To(ContainSubstring("left incomplete by an earlier run"))
		Expect(HasDay(dir, daysAgo(30))).To(BeFalse(), "the visible half must go with the hidden one")
		Expect(HasDay(dir, daysAgo(20))).To(BeTrue(), "a day nobody was purging must be left alone")
	})

	It("deletes the oldest day first and stops as soon as the target is met", func() {
		writeDay(dir, daysAgo(30), "a")
		writeDay(dir, daysAgo(20), "b")
		writeDay(dir, daysAgo(10), "c")
		target := baseFree + dayBytes(dir, daysAgo(30))
		newFakeVolume(dir, baseFree).install()

		Expect(PurgeToFreeSpace(dir, target, 7)).To(Succeed())

		Expect(HasDay(dir, daysAgo(30))).To(BeFalse())
		Expect(HasDay(dir, daysAgo(20))).To(BeTrue(), "one day was enough; nothing else should go")
		Expect(HasDay(dir, daysAgo(10))).To(BeTrue())
	})

	// One day short of the target. With the spec above, this pins the stopping point to the
	// target rather than to a fixed count.
	It("keeps deleting days until the target is met", func() {
		writeDay(dir, daysAgo(30), "a")
		writeDay(dir, daysAgo(20), "b")
		writeDay(dir, daysAgo(10), "c")
		target := baseFree + dayBytes(dir, daysAgo(30)) + dayBytes(dir, daysAgo(20))
		newFakeVolume(dir, baseFree).install()

		Expect(PurgeToFreeSpace(dir, target, 7)).To(Succeed())

		Expect(HasDay(dir, daysAgo(30))).To(BeFalse())
		Expect(HasDay(dir, daysAgo(20))).To(BeFalse())
		Expect(HasDay(dir, daysAgo(10))).To(BeTrue())
	})

	// The target is exactly the day's total size, so any segment left behind drags the purge
	// into the next day.
	It("deletes every segment of a day, compressed or not", func() {
		old := daysAgo(30)
		writeDay(dir, old, "a")
		writeDay(dir, old, "b")
		plain := filepath.Join(dayDir(dir, old), segmentPrefix(old)+"003"+plainReportFileExt)
		Expect(os.WriteFile(plain, []byte("{}\n"), consts.FilePermissions)).To(Succeed())
		Expect(DaySegmentPaths(dir, old)).To(HaveLen(3))
		writeDay(dir, daysAgo(20), "c")
		target := baseFree + dayBytes(dir, old)
		newFakeVolume(dir, baseFree).install()

		Expect(PurgeToFreeSpace(dir, target, 7)).To(Succeed())

		Expect(DaySegmentPaths(dir, old)).To(BeEmpty())
		Expect(HasDay(dir, daysAgo(20))).To(BeTrue())
	})

	// The floor is absolute: daysAgo(7) survives a target that can never be met.
	It("never deletes a day inside the minimum retention, and logs when the target stays unmet", func() {
		writeDay(dir, daysAgo(8), "a")
		writeDay(dir, daysAgo(7), "b")
		writeDay(dir, daysAgo(1), "c")
		newFakeVolume(dir, 0).install()

		out := captureLog(func() {
			Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())
		})

		Expect(HasDay(dir, daysAgo(8))).To(BeFalse())
		Expect(HasDay(dir, daysAgo(7))).To(BeTrue())
		Expect(HasDay(dir, daysAgo(1))).To(BeTrue())
		Expect(out).To(ContainSubstring("WARNING"))
		Expect(out).To(ContainSubstring("7-day minimum retention"))
	})

	It("logs what it deleted and the free space that resulted", func() {
		writeDay(dir, daysAgo(30), "a")
		writeDay(dir, daysAgo(20), "b")
		target := baseFree + dayBytes(dir, daysAgo(30))
		newFakeVolume(dir, baseFree).install()

		out := captureLog(func() {
			Expect(PurgeToFreeSpace(dir, target, 7)).To(Succeed())
		})

		Expect(out).To(ContainSubstring("Purged 1 report day(s), 1 segment(s)"))
		Expect(out).To(ContainSubstring("MiB now free"))
		Expect(out).ToNot(ContainSubstring("WARNING"))
	})

	It("removes directories left empty by the purge", func() {
		old := daysAgo(400) // a different year
		writeDay(dir, old, "b")
		monthDir := filepath.Dir(DaySegmentPaths(dir, old)[0])
		yearDir := filepath.Dir(monthDir)
		newFakeVolume(dir, 0).install()

		Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())

		_, err := os.Stat(monthDir)
		Expect(os.IsNotExist(err)).To(BeTrue())
		_, err = os.Stat(yearDir)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	// Pruning must stop above the reports root, where ingest keeps its lock file. The lock file
	// is removed here so the spec fails if pruning goes too far.
	It("keeps the reports root even when the purge leaves it empty", func() {
		writeDay(dir, daysAgo(400), "b")
		reportsDir := filepath.Join(dir, consts.ReportsDir)
		Expect(os.Remove(filepath.Join(reportsDir, lockFileName))).To(Succeed())
		newFakeVolume(dir, 0).install()

		Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())

		entries, err := os.ReadDir(reportsDir)
		Expect(err).ToNot(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})

	It("leaves the writer lock file alone", func() {
		writeDay(dir, daysAgo(30), "b")
		newFakeVolume(dir, 0).install()

		Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())

		_, err := os.Stat(filepath.Join(dir, consts.ReportsDir, lockFileName))
		Expect(err).ToNot(HaveOccurred())
	})

	// The warning must not blame retention when every day that could go, went.
	It("does not blame the minimum retention when there is nothing left to purge", func() {
		writeDay(dir, daysAgo(30), "a")
		writeDay(dir, daysAgo(20), "b")
		newFakeVolume(dir, 0).install()

		out := captureLog(func() {
			Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())
		})

		Expect(HasDay(dir, daysAgo(30))).To(BeFalse())
		Expect(HasDay(dir, daysAgo(20))).To(BeFalse())
		Expect(out).To(ContainSubstring("WARNING"))
		Expect(out).To(ContainSubstring("no report days left to delete"))
		Expect(out).ToNot(ContainSubstring("minimum retention"))
	})

	// A day deleted segment by segment leaves half a day that HasDay still reports, which a
	// backfill would then summarize. The assertion that matters is the on-disk state.
	Describe("a segment that cannot be deleted", func() {
		// failRemoving fails one unlink while its siblings succeed. It matches on file name,
		// not path, so it lands whether or not the purge hid the segment first.
		failRemoving := func(segment string) {
			GinkgoHelper()
			name := filepath.Base(segment)
			prev := removeFile
			removeFile = func(path string) error {
				if strings.HasSuffix(path, name) {
					return errors.New("unlink boom")
				}
				return prev(path)
			}
			DeferCleanup(func() { removeFile = prev })
		}

		It("leaves no part of that day visible to summarization", func() {
			old := daysAgo(30)
			writeDay(dir, old, "a")
			writeDay(dir, old, "b")
			segments := DaySegmentPaths(dir, old)
			Expect(segments).To(HaveLen(2))
			// The second, so the first is already deleted when the failure lands.
			failRemoving(segments[1])
			newFakeVolume(dir, 0).install()

			var err error
			out := captureLog(func() { err = PurgeToFreeSpace(dir, unreachableTarget, 7) })

			Expect(err).To(MatchError(ContainSubstring("unlink boom")))
			Expect(HasDay(dir, old)).To(BeFalse(),
				"a day that could not be fully deleted must not stay summarizable")
			Expect(DaySegmentPaths(dir, old)).To(BeEmpty())
			Expect(out).To(ContainSubstring(old.Format(consts.DateFormat)))
			// One of its two segments is still on disk, hidden.
			Expect(out).ToNot(ContainSubstring("Purged 1 report day(s)"))
		})

		// Later days are on the same volume and would fail the same way.
		It("stops the purge instead of moving on to the next day", func() {
			writeDay(dir, daysAgo(30), "a")
			writeDay(dir, daysAgo(20), "b")
			failRemoving(DaySegmentPaths(dir, daysAgo(30))[0])
			newFakeVolume(dir, 0).install()

			Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).ToNot(Succeed())

			Expect(HasDay(dir, daysAgo(20))).To(BeTrue())
		})

		// The warning must not claim there is nothing left to delete when the tree is intact.
		It("does not claim there are no report days left", func() {
			writeDay(dir, daysAgo(30), "a")
			failRemoving(DaySegmentPaths(dir, daysAgo(30))[0])
			newFakeVolume(dir, 0).install()

			var err error
			out := captureLog(func() { err = PurgeToFreeSpace(dir, unreachableTarget, 7) })

			Expect(err).To(HaveOccurred())
			Expect(out).ToNot(ContainSubstring("no report days left"))
			Expect(out).To(ContainSubstring("unlink boom"))
		})

		// Nothing but the purge can see a hidden segment, so nothing else reclaims its space.
		It("is deleted by a later purge", func() {
			old := daysAgo(30)
			writeDay(dir, old, "a")
			hidden := abandonedSegment(dir, old, 5)
			newFakeVolume(dir, 0).install()

			Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())

			_, statErr := os.Stat(hidden)
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "space no reader can see is space nothing else frees")
		})

		// The loop must re-probe after the repair, or it deletes a day to reach a target the
		// repair already met. The hidden segment is on a day whose visible siblings were all
		// unlinked before the crash, so finishing it frees space without taking a whole day.
		It("does not delete a day for space the repair had already freed", func() {
			old := daysAgo(30)
			writeDay(dir, old, "a")
			hidden := abandonedSegment(dir, daysAgo(40), 4096)
			target := baseFree + 4096
			newFakeVolume(dir, baseFree).install()

			Expect(PurgeToFreeSpace(dir, target, 7)).To(Succeed())

			_, statErr := os.Stat(hidden)
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "the repair must have run")
			Expect(HasDay(dir, old)).To(BeTrue(), "the target was met before any day needed to go")
		})

		// The lock file is invisible to readers too. Unlinking it under a live ingest lets a
		// second process take a fresh lock and interleave two gzip streams into one segment.
		It("never sweeps the writer lock file", func() {
			old := daysAgo(30)
			writeDay(dir, old, "a")
			lock := filepath.Join(dir, consts.ReportsDir, lockFileName)
			// The sweep has to actually run for this to mean anything.
			hidden := abandonedSegment(dir, old, 16)
			newFakeVolume(dir, 0).install()

			Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())

			_, statErr := os.Stat(hidden)
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "the sweep must have run")
			_, statErr = os.Stat(lock)
			Expect(statErr).ToNot(HaveOccurred(), "the sweep must delete report segments, not every dotfile")
		})
	})

	// When a segment cannot even be hidden, nothing has been unlinked, so the day must come
	// back whole.
	It("restores a day it started to hide but could not delete", func() {
		old := daysAgo(30)
		writeDay(dir, old, "a")
		writeDay(dir, old, "b")
		segments := DaySegmentPaths(dir, old)
		Expect(segments).To(HaveLen(2))
		// Renaming onto a directory fails, and by then the first segment is already hidden.
		blocked := filepath.Join(filepath.Dir(segments[1]), ".purging-"+filepath.Base(segments[1]))
		Expect(os.Mkdir(blocked, consts.DirPermissions)).To(Succeed())
		newFakeVolume(dir, 0).install()

		var err error
		out := captureLog(func() { err = PurgeToFreeSpace(dir, unreachableTarget, 7) })

		Expect(err).To(HaveOccurred())
		Expect(DaySegmentPaths(dir, old)).To(Equal(segments),
			"nothing was unlinked, so the day must be left exactly as it was")
		seq, _, readErr := ReadDay(dir, old)
		Expect(readErr).ToNot(HaveOccurred())
		Expect(collectIDs(seq)).To(ConsistOf("a", "b"))
		Expect(out).To(ContainSubstring(old.Format(consts.DateFormat)))
	})

	// Without a free-space reading there is no way to know what, if anything, needs to go.
	It("deletes nothing and reports the error when the space probe fails", func() {
		writeDay(dir, daysAgo(30), "a")
		vol := newFakeVolume(dir, 0)
		vol.err = errors.New("statfs boom")
		vol.install()

		Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(MatchError(ContainSubstring("statfs boom")))
		Expect(HasDay(dir, daysAgo(30))).To(BeTrue())
	})

	// After a failed probe the only reading in hand predates the deletions just made.
	It("reports no free-space figure when the probe fails mid-purge", func() {
		writeDay(dir, daysAgo(30), "a")
		writeDay(dir, daysAgo(20), "b")
		vol := newFakeVolume(dir, 0)
		vol.err = errors.New("statfs boom")
		vol.failAfter = 2 // the opening probe succeeds; the one after the first day does not
		vol.install()

		var err error
		out := captureLog(func() { err = PurgeToFreeSpace(dir, unreachableTarget, 7) })

		Expect(err).To(MatchError(ContainSubstring("statfs boom")))
		Expect(HasDay(dir, daysAgo(30))).To(BeFalse())
		Expect(out).To(ContainSubstring("Purged 1 report day(s), 1 segment(s) before the free-space probe failed"))
		Expect(out).ToNot(ContainSubstring("MiB"))
	})
})

var _ = Describe("statfsFreeBytes", func() {
	// The real probe. It only has to be a plausible, non-zero reading.
	It("reports a plausible free-space figure for a real path", func() {
		free, err := statfsFreeBytes(GinkgoT().TempDir())

		Expect(err).ToNot(HaveOccurred())
		Expect(free).To(BeNumerically(">", uint64(1)<<20))
	})

	It("returns an error for a path that does not exist", func() {
		_, err := statfsFreeBytes(filepath.Join(GinkgoT().TempDir(), "no-such-dir"))

		Expect(err).To(HaveOccurred())
	})
})
