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

// readLines streams the raw JSON lines of a day's report segments, oldest segment first.
//
// The yielded slice is only valid until the next iteration — decode it immediately, never
// retain it.
//
// The lines of every segment form one continuous sequence: consumers that number lines by
// position get numbering that does not restart or skip at a segment boundary. Because
// segments are append-only and ordered, a position is stable across reads even while ingest
// is still writing — new records only ever land after the last position of the previous read.
//
// Only complete, newline-terminated lines are yielded. A truncated tail (unclean shutdown,
// or the live segment being written right now) is not an error: everything complete is
// yielded and the half-written remainder is dropped.
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

// readSegment streams one segment's lines. It returns false when the consumer stopped, which
// must halt the whole day: yielding again after that panics the range-over-func loop.
//
// A segment that cannot be opened or has no readable gzip data is logged and skipped, so one
// damaged file does not hide the rest of the day.
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
			log.Printf("Report segment %s has no readable gzip data: %s", path, err) //#nosec G706 -- path is derived from controlled inputs
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
		// An unterminated member reports its error from Close as well as from the reads.
		if closeErr := gz.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil && !isTruncatedTail(err) {
		log.Printf("Report segment %s ends with unreadable data, using the records read so far: %s", path, err) //#nosec G706 -- path is derived from controlled inputs
	}
	return true
}

// isTruncatedTail reports whether err is the ordinary end of a segment whose gzip member was
// never terminated. That happens on every read of the segment ingest currently has open
// (flushed, but the trailer is only written on Close) and on every read of a segment left by
// a killed process. Neither is corruption, so neither is worth logging.
func isTruncatedTail(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF)
}

// scanCompleteLines is bufio.ScanLines without its final-token behaviour: a trailing chunk
// with no newline is a line the writer never finished, so it is dropped rather than handed
// to a consumer as if it were a record.
func scanCompleteLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), nil, bufio.ErrFinalToken
	}
	return 0, nil, nil
}

// ReadDay streams every record of a day's report segments, in the order they were written.
// Lines that do not decode are skipped, so one bad line does not cost the rest of the day.
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
