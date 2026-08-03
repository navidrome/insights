package store

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Record", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	// touch creates an empty file at path, including its parent directories.
	touch := func(path string) {
		GinkgoHelper()
		Expect(os.MkdirAll(filepath.Dir(path), 0750)).To(Succeed())
		Expect(os.WriteFile(path, []byte{}, 0600)).To(Succeed())
	}

	// dayFile is the path of a file named name inside testDay's directory.
	dayFile := func(name string) string {
		return filepath.Join(dir, "reports", "2026", "08", name)
	}

	Describe("NextSegmentPath", func() {
		It("builds a year/month nested path for the UTC day", func() {
			p, err := NextSegmentPath(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			Expect(p).To(Equal(dayFile("reports-2026-08-03.001.ndjson.gz")))
		})

		It("uses the UTC day, not the local day", func() {
			// 2026-08-02 21:30 in UTC-4 is already 2026-08-03 in UTC.
			t := time.Date(2026, 8, 2, 21, 30, 0, 0, time.FixedZone("test", -4*60*60))
			p, err := NextSegmentPath(dir, t)
			Expect(err).ToNot(HaveOccurred())
			Expect(p).To(ContainSubstring("reports-2026-08-03.001"))
		})

		It("skips indexes already on disk", func() {
			touch(dayFile("reports-2026-08-03.001.ndjson.gz"))
			touch(dayFile("reports-2026-08-03.002.ndjson.gz"))
			p, err := NextSegmentPath(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			Expect(p).To(Equal(dayFile("reports-2026-08-03.003.ndjson.gz")))
		})

		It("does not reuse an index taken by an uncompressed segment", func() {
			touch(dayFile("reports-2026-08-03.001.ndjson"))
			p, err := NextSegmentPath(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			Expect(p).To(Equal(dayFile("reports-2026-08-03.002.ndjson.gz")))
		})

		It("is unaffected by segments of another day", func() {
			touch(dayFile("reports-2026-08-04.001.ndjson.gz"))
			p, err := NextSegmentPath(dir, testDay)
			Expect(err).ToNot(HaveOccurred())
			Expect(p).To(Equal(dayFile("reports-2026-08-03.001.ndjson.gz")))
		})
	})

	Describe("DaySegmentPaths", func() {
		It("returns nothing when the day has no segments", func() {
			Expect(DaySegmentPaths(dir, testDay)).To(BeEmpty())
		})

		It("returns segments in ascending index order", func() {
			// Created out of order: the result must not depend on creation order.
			touch(dayFile("reports-2026-08-03.003.ndjson.gz"))
			touch(dayFile("reports-2026-08-03.001.ndjson.gz"))
			touch(dayFile("reports-2026-08-03.002.ndjson.gz"))
			Expect(DaySegmentPaths(dir, testDay)).To(Equal([]string{
				dayFile("reports-2026-08-03.001.ndjson.gz"),
				dayFile("reports-2026-08-03.002.ndjson.gz"),
				dayFile("reports-2026-08-03.003.ndjson.gz"),
			}))
		})

		It("includes uncompressed segments", func() {
			touch(dayFile("reports-2026-08-03.001.ndjson"))
			Expect(DaySegmentPaths(dir, testDay)).To(Equal([]string{
				dayFile("reports-2026-08-03.001.ndjson"),
			}))
		})

		It("excludes other days and unrelated files", func() {
			touch(dayFile("reports-2026-08-03.001.ndjson.gz"))
			touch(dayFile("reports-2026-08-04.001.ndjson.gz"))
			touch(dayFile("reports-2026-08-03.ndjson.gz"))      // no segment index
			touch(dayFile("reports-2026-08-03.1.ndjson.gz"))    // index not zero-padded
			touch(dayFile("reports-2026-08-03.001.ndjson.tmp")) // not a report file
			touch(dayFile("reports-2026-08-03.old.ndjson.gz"))  // index not numeric
			Expect(DaySegmentPaths(dir, testDay)).To(Equal([]string{
				dayFile("reports-2026-08-03.001.ndjson.gz"),
			}))
		})
	})

	Describe("HasDay", func() {
		It("returns false when no segment exists", func() {
			Expect(HasDay(dir, testDay)).To(BeFalse())
		})

		It("returns true for a compressed segment", func() {
			touch(dayFile("reports-2026-08-03.001.ndjson.gz"))
			Expect(HasDay(dir, testDay)).To(BeTrue())
		})

		It("returns true for an uncompressed segment", func() {
			touch(dayFile("reports-2026-08-03.001.ndjson"))
			Expect(HasDay(dir, testDay)).To(BeTrue())
		})

		It("returns false when only another day has segments", func() {
			touch(dayFile("reports-2026-08-04.001.ndjson.gz"))
			Expect(HasDay(dir, testDay)).To(BeFalse())
		})
	})
})
