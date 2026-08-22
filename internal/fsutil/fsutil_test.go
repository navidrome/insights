package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/navidrome/insights/internal/fsutil"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFsutil(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Fsutil Suite")
}

var _ = Describe("WriteFileAtomic", func() {
	var dir string
	var path string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		path = filepath.Join(dir, "charts.json")
	})

	// Nothing but the target should survive a temp-file write.
	entries := func() []string {
		GinkgoHelper()
		found, err := os.ReadDir(dir)
		Expect(err).ToNot(HaveOccurred())
		names := make([]string, 0, len(found))
		for _, e := range found {
			names = append(names, e.Name())
		}
		return names
	}

	It("writes content that reads back", func() {
		Expect(fsutil.WriteFileAtomic(path, []byte("hello"), 0600)).To(Succeed())

		Expect(os.ReadFile(path)).To(Equal([]byte("hello"))) //#nosec G304 -- test-only path from the suite's TempDir
	})

	It("leaves no temporary file behind", func() {
		Expect(fsutil.WriteFileAtomic(path, []byte("hello"), 0600)).To(Succeed())

		Expect(entries()).To(ConsistOf("charts.json"))
	})

	// os.CreateTemp always makes its file 0600, so the mode has to be set explicitly.
	It("uses the permissions it was given, not the temp file's", func() {
		Expect(fsutil.WriteFileAtomic(path, []byte("hello"), 0644)).To(Succeed())

		info, err := os.Stat(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0644)))
	})

	// The reason this exists: an O_TRUNC rewrite empties the file in place, so a reader sees
	// zero bytes, then a prefix, then the whole thing.
	It("replaces the file by rename rather than truncating it in place", func() {
		Expect(os.WriteFile(path, []byte("old"), 0600)).To(Succeed())
		before, err := os.Stat(path)
		Expect(err).ToNot(HaveOccurred())

		Expect(fsutil.WriteFileAtomic(path, []byte("new"), 0600)).To(Succeed())

		after, err := os.Stat(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(os.SameFile(before, after)).To(BeFalse(),
			"the name must point at a different, already complete file, not at the same one rewritten")
		Expect(os.ReadFile(path)).To(Equal([]byte("new"))) //#nosec G304 -- test-only path from the suite's TempDir
	})

	// A failed publish must cost nothing: old contents readable, no debris left behind.
	// Renaming onto a directory is the portable way to provoke it.
	It("leaves the original intact and no debris when the write fails", func() {
		target := filepath.Join(dir, "in-the-way")
		Expect(os.Mkdir(target, 0750)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(target, "child"), []byte("x"), 0600)).To(Succeed())

		Expect(fsutil.WriteFileAtomic(target, []byte("new"), 0600)).ToNot(Succeed())

		Expect(entries()).To(ConsistOf("in-the-way"))
		Expect(os.ReadFile(filepath.Join(target, "child"))).To(Equal([]byte("x"))) //#nosec G304 -- test-only path from the suite's TempDir
	})
})
