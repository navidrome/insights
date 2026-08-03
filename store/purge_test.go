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

// baseFree is the free space the fake volume starts with in every spec: a round number well
// clear of the byte counts the report tree contributes, so a target expressed as
// baseFree + <bytes of some days> is met by exactly those days and no others.
const baseFree = uint64(1) << 30

// unreachableTarget is more free space than any spec's report tree can release, so the purge
// runs until the minimum-retention floor stops it.
const unreachableTarget = uint64(1) << 40

// fakeVolume simulates the volume holding the data folder. Free space starts at freeAtStart
// and rises by exactly the bytes the purge frees, because every probe re-measures the report
// tree on disk.
//
// That modelling is the point. A probe returning a constant below the target makes "delete
// until the target is met" and "delete every day down to the retention floor" produce the same
// result, and every spec here would pass against a purge that never re-checks and never stops
// early.
type fakeVolume struct {
	dir         string
	freeAtStart uint64
	usedAtStart uint64
	probes      int
	err         error
	// failAfter is the probe number err starts being returned from, counting from 1. Zero
	// means the very first probe fails.
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

// reportBytes is the size of everything under the report tree, lock file included. Only its
// changes matter to fakeVolume, and the lock file's contribution is constant.
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

// dayBytes is the size of one day's segments — what the volume gains when that day is purged.
// It matches on the day's file prefix rather than going through DaySegmentPaths, so a segment
// that exists in both compressed and plain form still counts once per file, exactly as the
// purge deletes it.
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

// abandonedSegment writes a hidden segment of the given size into date's day directory: what an
// earlier purge leaves behind when it hid a day's segments and then failed to unlink one. No
// reader can see it, which is why the purge has to sweep it up itself.
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

	// DATA_FOLDER is documented as defaulting to the current directory, and both ingest and
	// summarization take "" to mean that. statfs is the one caller that cannot: Statfs("")
	// fails with ENOENT, so retention would never run and the hourly job would log the same
	// error forever. The real probe is used deliberately — a fake would not exercise it — with
	// a target of one byte, so the purge returns on the free-space check without touching the
	// tree the test process happens to be running in.
	It("probes the current directory when the data folder is empty", func() {
		Expect(PurgeToFreeSpace("", 1, 7)).To(Succeed())
	})

	It("succeeds when the reports directory does not exist", func() {
		newFakeVolume(dir, 0).install()

		Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())
	})

	// Exactly at the target, not above it: this also pins the comparison to >=, so a purge that
	// deletes a day whenever free space merely equals the target fails here.
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
		Expect(vol.probes).To(Equal(1), "it should not even enumerate the tree")
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

	// The companion to the spec above: one day short of the target, so the purge has to keep
	// going. Together they pin the stopping point to the target rather than to a fixed count.
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

	// A day is one segment per writer session, and a segment may have been decompressed by
	// hand. Purging a day takes all of them: the target here is exactly the day's total size,
	// so leaving any segment behind both fails the count below and drags the purge into the
	// next day.
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

	// The floor is absolute: daysAgo(7) survives a target that can never be met, and the operator
	// is told, because at that point the disk is being filled by something else.
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

	// The reports root is where ingest creates its lock file and today's day directory, so
	// pruning must stop above it. The lock file is removed here on purpose: while it exists the
	// root is never empty, and this spec would hold no matter what pruning did.
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

	// The warning fires for any unmet target, and "nothing left to purge" is one of the ways to
	// get there. Blaming retention then sends the operator to look at a knob that is not the
	// problem — every day that could go, went.
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

	// A day deleted segment by segment leaves, on the first failure, a day that is half on disk
	// and still reported by HasDay. A later `process -once -days N` backfill then rewrites that
	// day's summary from the reports that survived and publishes the result — silent corruption
	// of the charts, not just of the raw store.
	//
	// The assertion that matters is the on-disk state: an error alone would be returned by a
	// purge that left the day exactly as broken.
	Describe("a segment that cannot be deleted", func() {
		// failRemoving makes unlinking one segment fail — the way a single bad inode, or a file
		// something else is holding, does — while its siblings delete normally. It matches on
		// the segment's file name, not its full path, so the failure lands whether the purge
		// unlinks the segment under its own name or under the one it hides it behind first.
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
			// The second segment, so the first one is already deleted when the failure lands:
			// that is the state that used to leave a visible, half-empty day behind.
			failRemoving(segments[1])
			newFakeVolume(dir, 0).install()

			var err error
			out := captureLog(func() { err = PurgeToFreeSpace(dir, unreachableTarget, 7) })

			Expect(err).To(MatchError(ContainSubstring("unlink boom")))
			Expect(HasDay(dir, old)).To(BeFalse(),
				"a day that could not be fully deleted must not stay summarizable")
			Expect(DaySegmentPaths(dir, old)).To(BeEmpty())
			Expect(out).To(ContainSubstring(old.Format(consts.DateFormat)))
			// One of its two segments is still on disk, hidden. Counting it as a day purged
			// would put "Purged 1 report day(s)" in the log for a day that is still there.
			Expect(out).ToNot(ContainSubstring("Purged 1 report day(s)"))
		})

		// The days after this one are on the same volume and would fail the same way, and each
		// attempt is another day left in a state an operator has to be told about.
		It("stops the purge instead of moving on to the next day", func() {
			writeDay(dir, daysAgo(30), "a")
			writeDay(dir, daysAgo(20), "b")
			failRemoving(DaySegmentPaths(dir, daysAgo(30))[0])
			newFakeVolume(dir, 0).install()

			Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).ToNot(Succeed())

			Expect(HasDay(dir, daysAgo(20))).To(BeTrue())
		})

		// The old warning fired on any unmet target and blamed the only two causes it knew
		// about. On a volume that refuses deletions it told an operator there were no report
		// days left to delete while the entire tree was still there.
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

		// Hidden segments are invisible to HasDay and to the day enumeration by design, which
		// also means nothing else would ever reclaim their space — on a store whose retention
		// is driven by free space, that is a leak that grows with every failure.
		It("is deleted by a later purge", func() {
			old := daysAgo(30)
			writeDay(dir, old, "a")
			hidden := abandonedSegment(dir, old, 5)
			newFakeVolume(dir, 0).install()

			Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())

			_, statErr := os.Stat(hidden)
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "space no reader can see is space nothing else frees")
		})

		// The sweep frees space before the day loop runs, so the loop has to look at the volume
		// again before it deletes anything. Without that it destroys a day of reports to reach
		// a target the sweep had already reached.
		It("does not delete a day for space the sweep had already freed", func() {
			old := daysAgo(30)
			writeDay(dir, old, "a")
			hidden := abandonedSegment(dir, old, 4096)
			target := baseFree + 4096
			newFakeVolume(dir, baseFree).install()

			Expect(PurgeToFreeSpace(dir, target, 7)).To(Succeed())

			_, statErr := os.Stat(hidden)
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "the sweep must have run")
			Expect(HasDay(dir, old)).To(BeTrue(), "the target was met before any day needed to go")
		})

		// The sweep deletes files no reader can see, and from a reader's point of view the
		// writer's lock file is exactly that. Unlinking it under a live ingest breaks the
		// single-writer invariant in silence: the next process to start creates a fresh lock
		// file, takes it uncontested, and appends to the same day as the process still holding
		// the old inode — two gzip streams interleaved into one segment.
		It("never sweeps the writer lock file", func() {
			old := daysAgo(30)
			writeDay(dir, old, "a")
			lock := filepath.Join(dir, consts.ReportsDir, lockFileName)
			// The lock file has to be reachable by the sweep for this to mean anything, so the
			// purge is put under pressure and given something to sweep.
			hidden := abandonedSegment(dir, old, 16)
			newFakeVolume(dir, 0).install()

			Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(Succeed())

			_, statErr := os.Stat(hidden)
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "the sweep must have run")
			_, statErr = os.Stat(lock)
			Expect(statErr).ToNot(HaveOccurred(), "the sweep must delete report segments, not every dotfile")
		})
	})

	// The other half of the two-pass deletion: when a segment cannot even be hidden, nothing
	// has been unlinked yet, so the day has to come back whole rather than half-hidden.
	It("restores a day it started to hide but could not delete", func() {
		old := daysAgo(30)
		writeDay(dir, old, "a")
		writeDay(dir, old, "b")
		segments := DaySegmentPaths(dir, old)
		Expect(segments).To(HaveLen(2))
		// A directory sitting on the name the purge hides the second segment under: renaming a
		// file onto a directory fails, and by then the first segment is already hidden.
		blocked := filepath.Join(filepath.Dir(segments[1]), ".purging-"+filepath.Base(segments[1]))
		Expect(os.Mkdir(blocked, consts.DirPermissions)).To(Succeed())
		newFakeVolume(dir, 0).install()

		var err error
		out := captureLog(func() { err = PurgeToFreeSpace(dir, unreachableTarget, 7) })

		Expect(err).To(HaveOccurred())
		Expect(DaySegmentPaths(dir, old)).To(Equal(segments),
			"nothing was unlinked, so the day must be left exactly as it was")
		seq, readErr := ReadDay(dir, old)
		Expect(readErr).ToNot(HaveOccurred())
		Expect(collectIDs(seq)).To(ConsistOf("a", "b"))
		Expect(out).To(ContainSubstring(old.Format(consts.DateFormat)))
	})

	// An unreadable volume is not a licence to start deleting: without a free-space reading
	// there is no way to know how much, if anything, needs to go.
	It("deletes nothing and reports the error when the space probe fails", func() {
		writeDay(dir, daysAgo(30), "a")
		vol := newFakeVolume(dir, 0)
		vol.err = errors.New("statfs boom")
		vol.install()

		Expect(PurgeToFreeSpace(dir, unreachableTarget, 7)).To(MatchError(ContainSubstring("statfs boom")))
		Expect(HasDay(dir, daysAgo(30))).To(BeTrue())
	})

	// When the probe fails mid-purge the only free-space reading in hand predates the deletions
	// just made. Printing it would report a number that is both stale and, in the one case where
	// the true value is unknown, presented as if it had been measured.
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
	// The real probe, on a path that certainly exists. It only has to be a plausible, non-zero
	// reading below the size of any real volume's total capacity — the exact number is compared
	// against df by hand, since nothing here knows what the disk should have free.
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
