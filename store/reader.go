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

	"github.com/navidrome/insights/consts"
)

// readLines streams a day's segments as one continuous sequence, oldest first. Only complete
// lines are yielded; a truncated tail is not an error. The yielded slice is valid only until
// the next iteration, and a line's position is stable across reads.
func readLines(dataFolder string, date time.Time) (iter.Seq[[]byte], error) {
	paths := DaySegmentPaths(dataFolder, date)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no report segment for %s", date.UTC().Format(consts.DateFormat))
	}

	return func(yield func([]byte) bool) {
		for _, path := range paths {
			if !readSegment(path, yield) {
				return
			}
		}
	}, nil
}

// readSegment streams one segment's lines, returning false when the consumer stopped. A
// damaged or unopenable segment is logged and skipped rather than hiding the rest of the day.
func readSegment(path string, yield func([]byte) bool) bool {
	f, err := os.Open(path) //#nosec G304 -- path comes from a controlled directory listing
	if err != nil {
		log.Printf("Skipping unreadable report segment %s: %s", path, err) //#nosec G706 -- path is derived from controlled inputs
		return true
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	var gz *gzip.Reader
	if strings.HasSuffix(path, ".gz") {
		gz, err = gzip.NewReader(f)
		if err != nil {
			// A zero-byte segment is a process killed before its first flush, not damage, and
			// every summarization pass would log it again.
			if !isTruncatedTail(err) {
				log.Printf("Report segment %s has no readable gzip data: %s", path, err) //#nosec G706 -- path is derived from controlled inputs
			}
			return true
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
			return false
		}
	}

	err = sc.Err()
	if gz != nil {
		// An unterminated member reports its error from Close too.
		if closeErr := gz.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil && !isTruncatedTail(err) {
		log.Printf("Report segment %s ends with unreadable data, using the records read so far: %s", path, err) //#nosec G706 -- path is derived from controlled inputs
	}
	return true
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
// skipped.
func ReadDay(dataFolder string, date time.Time) (iter.Seq[Record], error) {
	lines, err := readLines(dataFolder, date)
	if err != nil {
		return nil, err
	}
	return func(yield func(Record) bool) {
		for line := range lines {
			var rec Record
			if err := json.Unmarshal(line, &rec); err != nil {
				log.Printf("Skipping malformed report line: %s", err)
				continue
			}
			if !yield(rec) {
				return
			}
		}
	}, nil
}
