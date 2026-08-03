package store

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Record", func() {
	Describe("DayFilePath", func() {
		It("builds a year/month nested path for the UTC day", func() {
			p := DayFilePath("/data", testDay)
			Expect(p).To(Equal(filepath.Join("/data", "reports", "2026", "08", "reports-2026-08-03.ndjson.gz")))
		})

		It("uses the UTC day, not the local day", func() {
			// 2026-08-03 01:30 UTC is still 2026-08-02 in UTC-4.
			t := time.Date(2026, 8, 2, 21, 30, 0, 0, time.FixedZone("test", -4*60*60))
			Expect(DayFilePath("/data", t)).To(ContainSubstring("reports-2026-08-03"))
		})
	})

	Describe("HasDay", func() {
		var dir string

		BeforeEach(func() {
			dir = GinkgoT().TempDir()
		})

		It("returns false when no file exists", func() {
			Expect(HasDay(dir, testDay)).To(BeFalse())
		})

		It("returns true for a compressed file", func() {
			path := DayFilePath(dir, testDay)
			Expect(os.MkdirAll(filepath.Dir(path), 0750)).To(Succeed())
			Expect(os.WriteFile(path, []byte{}, 0600)).To(Succeed())
			Expect(HasDay(dir, testDay)).To(BeTrue())
		})

		It("returns true for an uncompressed file", func() {
			path := plainDayFilePath(dir, testDay)
			Expect(os.MkdirAll(filepath.Dir(path), 0750)).To(Succeed())
			Expect(os.WriteFile(path, []byte{}, 0600)).To(Succeed())
			Expect(HasDay(dir, testDay)).To(BeTrue())
		})
	})
})
