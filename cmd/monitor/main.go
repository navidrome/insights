package main

import (
	"cmp"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/navidrome/insights/db"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

func main() {
	dbPath := flag.String("db", "", "Path to insights.db (default: $DATA_FOLDER/insights.db or ./insights.db)")
	dateStr := flag.String("date", "", "Date to query (YYYY-MM-DD format, default: latest date in DB)")
	flag.Parse()

	// Determine database path
	dbFile := *dbPath
	if dbFile == "" {
		dataFolder := cmp.Or(os.Getenv("DATA_FOLDER"), ".")
		dbFile = filepath.Join(dataFolder, "insights.db")
	}

	if err := run(dbFile, *dateStr); err != nil {
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

func run(dbPath, dateStr string) error {
	// Open database
	dbConn, err := db.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("opening database %s: %w", dbPath, err)
	}
	defer func() { _ = dbConn.Close() }()

	// Determine date to query
	var queryDate time.Time
	if dateStr != "" {
		queryDate, err = time.Parse("2006-01-02", dateStr)
		if err != nil {
			return fmt.Errorf("parsing date %q: %w", dateStr, err)
		}
	} else {
		// Get the latest date from the database
		var maxTime string
		err := dbConn.QueryRow("SELECT MAX(DATE(time)) FROM insights").Scan(&maxTime)
		if err != nil {
			return fmt.Errorf("getting latest date: %w", err)
		}
		if maxTime == "" {
			return fmt.Errorf("no data in database")
		}
		queryDate, err = time.Parse("2006-01-02", maxTime)
		if err != nil {
			return fmt.Errorf("parsing latest date %q: %w", maxTime, err)
		}
	}

	rows, err := db.SelectData(dbConn, queryDate)
	if err != nil {
		return fmt.Errorf("selecting data: %w", err)
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

	if s.numInstances == 0 {
		return fmt.Errorf("no data found for %s", queryDate.Format("2006-01-02"))
	}

	s.trackStats = calcTrackStats(trackValues)

	// Print output
	fmt.Printf("Date: %s\n", queryDate.Format("2006-01-02"))
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
