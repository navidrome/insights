package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/insights/consts"
)

// writeSegment puts content at the path a real segment of date would occupy, so the listing
// finds it and the reader has to open it.
func writeSegment(t *testing.T, dir string, date time.Time, content string) {
	t.Helper()
	name := "reports-" + date.Format(consts.DateFormat) + ".001" + consts.ReportFileExt
	path := filepath.Join(dir, consts.ReportsDir, date.Format("2006"), date.Format("01"), name)
	if err := os.MkdirAll(filepath.Dir(path), consts.DirPermissions); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), consts.FilePermissions); err != nil {
		t.Fatal(err)
	}
}

// A day that yields no instances because none of it could be read is not an empty day, and
// "no data found" is the one answer that is certainly wrong.
func TestRunReportsUnreadableDataRatherThanAnEmptyDay(t *testing.T) {
	dir := t.TempDir()
	date := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	writeSegment(t, dir, date, "not gzip at all\n")

	err := run(dir, date)

	if err == nil {
		t.Fatal("run succeeded on a day whose only segment is unreadable")
	}
	if strings.Contains(err.Error(), "no data found") {
		t.Errorf("run error = %v, want the read failure rather than an empty day", err)
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("run error = %v, want it to name the incomplete read", err)
	}
}

// A day nobody recorded and a day nobody can read are different problems, and this tool exists
// to tell an operator which one they have. HasDay reports both as false, so run must not use it.
func TestRunSeparatesAnUnreadableDayFromAnAbsentOne(t *testing.T) {
	date := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

	t.Run("absent", func(t *testing.T) {
		err := run(t.TempDir(), date)
		if err == nil || !strings.Contains(err.Error(), "no report file") {
			t.Errorf("run error = %v, want it to report an absent day", err)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		dir := t.TempDir()
		// A regular file where the month directory belongs, so listing fails with ENOTDIR.
		monthDir := filepath.Join(dir, consts.ReportsDir, "2026", "08")
		if err := os.MkdirAll(filepath.Dir(monthDir), consts.DirPermissions); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(monthDir, []byte("not a directory"), consts.FilePermissions); err != nil {
			t.Fatal(err)
		}

		err := run(dir, date)
		if err == nil {
			t.Fatal("run succeeded on an unreadable report directory")
		}
		if strings.Contains(err.Error(), "no report file") {
			t.Errorf("run error = %v, want it to name the listing failure, not an absent day", err)
		}
		if !strings.Contains(err.Error(), "listing report segments") {
			t.Errorf("run error = %v, want it to name the listing failure", err)
		}
	})
}
