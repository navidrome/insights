package main

import (
	"cmp"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/navidrome/insights/consts"
	"github.com/navidrome/insights/store"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

func main() {
	dataFolder := flag.String("data", "", "Data folder (default: $DATA_FOLDER or .)")
	dateStr := flag.String("date", "", "UTC day to report on, YYYY-MM-DD (default: today)")
	flag.Parse()

	folder := cmp.Or(*dataFolder, os.Getenv("DATA_FOLDER"), ".")

	date := time.Now().UTC().Truncate(24 * time.Hour)
	if *dateStr != "" {
		parsed, err := time.ParseInLocation(consts.DateFormat, *dateStr, time.UTC)
		if err != nil {
			log.Fatalf("Invalid -date %q: %v", *dateStr, err)
		}
		date = parsed
	}

	if err := run(folder, date); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

type stats struct {
	numInstances int64
	versions     map[string]uint64
	osTypes      map[string]uint64
	osArch       map[string]uint64
	trackStats   *trackStats
	zeroTracks   uint64
	millionPlus  uint64
}

type trackStats struct {
	Max  int64
	Mean float64
}

func run(dataFolder string, date time.Time) error {
	// Not HasDay: it reads a directory it cannot list as false, and reporting "no report file"
	// for a day whose segments are merely unreachable sends an operator looking in the wrong
	// place. This is the tool they reach for to find out which of the two it is.
	paths, err := store.DaySegmentPaths(dataFolder, date)
	if err != nil {
		return fmt.Errorf("listing report segments for %s: %w", date.Format(consts.DateFormat), err)
	}
	if len(paths) == 0 {
		return fmt.Errorf("no report file for %s", date.Format(consts.DateFormat))
	}

	// The snapshot just listed, so the day reported on is the day checked.
	rows, incomplete, err := store.LastPerIDFrom(paths)
	if err != nil {
		return fmt.Errorf("reading reports: %w", err)
	}

	// Collect statistics
	s := stats{
		versions: make(map[string]uint64),
		osTypes:  make(map[string]uint64),
		osArch:   make(map[string]uint64),
	}

	var trackValues []int64

	for data := range rows {
		s.numInstances++
		s.versions[mapVersion(data)]++

		osType, osArch := mapOSAndArch(data)
		s.osTypes[osType]++
		s.osArch[osArch]++

		// Track library size
		if data.Library.Tracks > 0 {
			trackValues = append(trackValues, data.Library.Tracks)
		}
		if data.Library.Tracks == 0 {
			s.zeroTracks++
		}
		if data.Library.Tracks >= 1000000 {
			s.millionPlus++
		}
	}

	// Before the zero-instance check, not after: a day whose every segment was unreadable also
	// yields no instances, and "no data found" is the one answer that is certainly wrong there.
	// The reader skips what it cannot read, so the counts would otherwise look like a smaller
	// day rather than a partial one.
	if err := incomplete(); err != nil {
		return fmt.Errorf("reports for %s are incomplete, the counts would be wrong: %w",
			date.Format(consts.DateFormat), err)
	}

	if s.numInstances == 0 {
		return fmt.Errorf("no data found for %s", date.Format(consts.DateFormat))
	}

	s.trackStats = calcTrackStats(trackValues)

	// Print output
	printStats(s)
	return nil
}

func printStats(s stats) {
	fmt.Printf("Total instances: %d\n\n", s.numInstances)

	// By Version - top 30
	fmt.Println("By Version:")
	printTopN(s.versions, 30)
	fmt.Println()

	// By OS
	fmt.Println("By OS:")
	printTopN(s.osTypes, 20)
	fmt.Println()

	// By OS/Architecture
	fmt.Println("By OS/Architecture:")
	printTopN(s.osArch, 20)
	fmt.Println()

	// Library sizes
	fmt.Println("Library sizes (tracks):")
	if s.trackStats != nil {
		fmt.Printf("  Largest: %d\n", s.trackStats.Max)
		fmt.Printf("  Average: %d\n", int64(math.Round(s.trackStats.Mean)))
	}
	fmt.Println()

	// Library size distribution
	fmt.Println("Library size distribution:")
	fmt.Printf("%6d | = 0 tracks\n", s.zeroTracks)
	fmt.Printf("%6d | > 1000000 tracks\n", s.millionPlus)
}

type kv struct {
	Key   string
	Value uint64
}

func printTopN(m map[string]uint64, n int) {
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	slices.SortFunc(pairs, func(a, b kv) int {
		return cmp.Compare(b.Value, a.Value)
	})

	limit := min(n, len(pairs))
	for i := 0; i < limit; i++ {
		fmt.Printf("%6d | %s\n", pairs[i].Value, pairs[i].Key)
	}
}

// Match the first 8 characters of a git sha
var versionRegex = regexp.MustCompile(`\(([0-9a-fA-F]{8})[0-9a-fA-F]*\)`)

// mapVersion normalizes version strings (truncate git sha to 8 chars)
func mapVersion(data insights.Data) string {
	return versionRegex.ReplaceAllString(data.Version, "($1)")
}

// mapOSAndArch returns the OS type and OS/Arch combination
func mapOSAndArch(data insights.Data) (osType, osArch string) {
	switch data.OS.Type {
	case "darwin":
		osType = "macOS"
	case "linux":
		if data.OS.Containerized {
			osType = "Linux (containerized)"
		} else {
			osType = "Linux"
		}
	case "windows":
		osType = "Windows"
	case "freebsd":
		osType = "FreeBSD"
	case "netbsd":
		osType = "NetBSD"
	case "openbsd":
		osType = "OpenBSD"
	default:
		osType = strings.Title(data.OS.Type) //nolint:staticcheck
	}

	// For arch, remove "(containerized)" suffix
	archOS := osType
	if strings.Contains(archOS, "(containerized)") {
		archOS = "Linux"
	}
	osArch = archOS + " " + data.OS.Arch

	return osType, osArch
}

// calcTrackStats computes max and mean for a slice of values
func calcTrackStats(values []int64) *trackStats {
	if len(values) == 0 {
		return nil
	}

	var sum, maxVal int64
	for _, v := range values {
		sum += v
		if v > maxVal {
			maxVal = v
		}
	}

	return &trackStats{
		Max:  maxVal,
		Mean: float64(sum) / float64(len(values)),
	}
}
