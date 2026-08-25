package store

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"os"
	"strings"
	"time"

	"github.com/navidrome/insights/internal/consts"
)

// readLines streams a day's segments as one continuous sequence, oldest first. Only complete
// lines are yielded; a truncated tail is not an error. The yielded slice is valid only until
// the next iteration, and a line's position is stable across reads.
//
// The second return reports, once iteration has finished, whether a segment could not be read
// in full. Anything derived from the whole day must check it: a skipped segment does not look
// like an error downstream, it looks like a smaller day.
func readLines(dataFolder string, date time.Time) (iter.Seq[[]byte], func() error, error) {
	paths, err := DaySegmentPaths(dataFolder, date)
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no report segment for %s", date.UTC().Format(consts.DateFormat))
	}
	seq, incomplete := readLinesFrom(paths)
	return seq, incomplete, nil
}

// readLinesFrom is readLines over a snapshot the caller already holds. Passes that match
// records by position have to share one snapshot: a segment leaving the listing between two
// independent listings does not just drop its own records, it shifts every position after it
// onto a different line.
func readLinesFrom(paths []string) (iter.Seq[[]byte], func() error) {
	var incomplete error
	seq := func(yield func([]byte) bool) {
		incomplete = nil
		for _, path := range paths {
			ok, err := readSegment(path, yield)
			incomplete = errors.Join(incomplete, err)
			if !ok {
				return
			}
		}
	}
	return seq, func() error { return incomplete }
}

// decodeFailures turns a count of undecodable lines into the verdict callers carry. A complete,
// newline-terminated line that is not JSON is not something the writer produces, so it means a
// record is gone rather than a payload this reader does not understand.
func decodeFailures(n int) error {
	if n == 0 {
		return nil
	}
	return fmt.Errorf("%d line(s) could not be decoded", n)
}

// readSegment streams one segment's lines. The bool is false when the consumer stopped; the
// error is set when the segment could not be read in full. A damaged segment is skipped rather
// than hiding the rest of the day, so both returns matter: reading continues, and the caller
// still learns the day is incomplete.
func readSegment(path string, yield func([]byte) bool) (bool, error) {
	f, err := os.Open(path) //#nosec G304 -- path comes from a controlled directory listing
	if err != nil {
		log.Printf("Skipping unreadable report segment %s: %s", path, err) //#nosec G706 -- path is derived from controlled inputs
		return true, fmt.Errorf("opening segment %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	var gz *gzip.Reader
	if strings.HasSuffix(path, ".gz") {
		gz, err = gzip.NewReader(f)
		if err != nil {
			// A zero-byte segment is a process killed before its first flush, not damage, and
			// every summarization pass would log it again.
			if isTruncatedTail(err) {
				return true, nil
			}
			log.Printf("Report segment %s has no readable gzip data: %s", path, err) //#nosec G706 -- path is derived from controlled inputs
			return true, fmt.Errorf("reading segment %s: %w", path, err)
		}
		r = gz
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), consts.MaxLineBytes)
	sc.Split(scanCompleteLines)
	for sc.Scan() {
		if !yield(sc.Bytes()) {
			if gz != nil {
				_ = gz.Close()
			}
			return false, nil
		}
	}

	err = sc.Err()
	if gz != nil {
		// An unterminated member reports its error from Close too.
		if closeErr := gz.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil || isTruncatedTail(err) {
		return true, nil
	}
	log.Printf("Report segment %s ends with unreadable data, using the records read so far: %s", path, err) //#nosec G706 -- path is derived from controlled inputs
	return true, fmt.Errorf("reading segment %s: %w", path, err)
}

// isTruncatedTail reports whether err is the ordinary end of a gzip member that was never
// terminated: the live segment, or one left by a killed process. Neither is corruption.
func isTruncatedTail(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// scanCompleteLines is bufio.ScanLines without its final token: a trailing chunk with no
// newline is a line the writer never finished.
func scanCompleteLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), nil, bufio.ErrFinalToken
	}
	return 0, nil, nil
}

// ReadDay streams every record of a day, in write order. Lines that do not decode are
// skipped. The second return reports whether a segment could not be read in full; see
// readLines.
func ReadDay(dataFolder string, date time.Time) (iter.Seq[Record], func() error, error) {
	lines, incomplete, err := readLines(dataFolder, date)
	if err != nil {
		return nil, nil, err
	}
	var malformed int
	seq := func(yield func(Record) bool) {
		malformed = 0
		for line := range lines {
			var rec Record
			if err := json.Unmarshal(line, &rec); err != nil {
				log.Printf("Skipping malformed report line: %s", err)
				malformed++
				continue
			}
			if !yield(rec) {
				return
			}
		}
	}
	return seq, func() error { return errors.Join(incomplete(), decodeFailures(malformed)) }, nil
}
