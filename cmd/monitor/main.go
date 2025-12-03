package main

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"iter"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/navidrome/insights/db"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

func main() {
	dbPath := flag.String("db", "", "Path to insights.db (default: $DATA_FOLDER/insights.db or ./insights.db)")
	flag.Parse()

	// Determine database path
	dbFile := *dbPath
	if dbFile == "" {
		dataFolder := cmp.Or(os.Getenv("DATA_FOLDER"), ".")
		dbFile = filepath.Join(dataFolder, "insights.db")
	}

	if err := run(dbFile); err != nil {
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

func run(dbPath string) error {
	// Open database
	dbConn, err := db.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("opening database %s: %w", dbPath, err)
	}
	defer func() { _ = dbConn.Close() }()

	// Query for last 24 hours - get the latest entry per instance ID
	rows, err := selectLast24Hours(dbConn)
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
		return fmt.Errorf("no data found in the last 24 hours")
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

// selectLast24Hours returns the latest entry per instance ID from the last 24 hours
func selectLast24Hours(dbConn *sql.DB) (iter.Seq[insights.Data], error) {
	query := `
SELECT i1.id, i1.time, i1.data
FROM insights i1
INNER JOIN (
    SELECT id, MAX(time) as max_time
    FROM insights
    WHERE time > datetime('now', '-24 hours')
    GROUP BY id
) i2 ON i1.id = i2.id AND i1.time = i2.max_time
WHERE i1.time > datetime('now', '-24 hours')
ORDER BY i1.id, i1.time DESC;`

	rows, err := dbConn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("querying data: %w", err)
	}

	return func(yield func(insights.Data) bool) {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id, t, j string
			if err := rows.Scan(&id, &t, &j); err != nil {
				log.Printf("Error scanning row: %s", err)
				return
			}
			var data insights.Data
			if err := json.Unmarshal([]byte(j), &data); err != nil {
				log.Printf("Error unmarshalling data: %s", err)
				return
			}
			if !yield(data) {
				return
			}
		}
	}, nil
}
