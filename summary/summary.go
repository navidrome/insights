package summary

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/navidrome/insights/db"
	"github.com/navidrome/navidrome/core/metrics/insights"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Stats holds statistical metrics for a numeric field
type Stats struct {
	Min    int64   `json:"min"`
	Max    int64   `json:"max"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	StdDev float64 `json:"stdDev"`
}

type Summary struct {
	NumInstances   int64             `json:"numInstances,omitempty"`
	NumActiveUsers int64             `json:"numActiveUsers,omitempty"`
	Versions       map[string]uint64 `json:"versions,omitempty"`
	OS             map[string]uint64 `json:"os,omitempty"`
	Distros        map[string]uint64 `json:"distros,omitempty"`
	PlayerTypes    map[string]uint64 `json:"playerTypes,omitempty"`
	Players        map[string]uint64 `json:"players,omitempty"`
	Users          map[string]uint64 `json:"users,omitempty"`
	Tracks         map[string]uint64 `json:"tracks,omitempty"`
	Albums         map[string]uint64 `json:"albums,omitempty"`
	Artists        map[string]uint64 `json:"artists,omitempty"`
	MusicFS        map[string]uint64 `json:"musicFS,omitempty"`
	DataFS         map[string]uint64 `json:"dataFS,omitempty"`
	TrackStats     *Stats            `json:"trackStats,omitempty"`
	AlbumStats     *Stats            `json:"albumStats,omitempty"`
	ArtistStats    *Stats            `json:"artistStats,omitempty"`
	PlaylistStats  *Stats            `json:"playlistStats,omitempty"`
	ShareStats     *Stats            `json:"shareStats,omitempty"`
	RadioStats     *Stats            `json:"radioStats,omitempty"`
	LibraryStats   *Stats            `json:"libraryStats,omitempty"`
}

func SummarizeData(dbConn *sql.DB, date time.Time) error {
	rows, err := db.SelectData(dbConn, date)
	if err != nil {
		log.Printf("Error selecting data: %s", err)
		return err
	}
	summary := Summary{
		Versions:    make(map[string]uint64),
		OS:          make(map[string]uint64),
		Distros:     make(map[string]uint64),
		PlayerTypes: make(map[string]uint64),
		Players:     make(map[string]uint64),
		Users:       make(map[string]uint64),
		Tracks:      make(map[string]uint64),
		Albums:      make(map[string]uint64),
		Artists:     make(map[string]uint64),
		MusicFS:     make(map[string]uint64),
		DataFS:      make(map[string]uint64),
	}

	// Collect values for statistics calculation
	var trackValues, albumValues, artistValues []int64
	var playlistValues, shareValues, radioValues, libraryValues []int64

	for data := range rows {
		// Summarize data here
		summary.NumInstances++
		summary.NumActiveUsers += data.Library.ActiveUsers
		summary.Versions[mapVersion(data)]++
		summary.OS[mapOS(data)]++
		if data.OS.Type == "linux" && !data.OS.Containerized {
			summary.Distros[data.OS.Distro]++
		}
		summary.Users[fmt.Sprintf("%d", data.Library.ActiveUsers)]++
		summary.MusicFS[mapFS(data.FS.Music)]++
		summary.DataFS[mapFS(data.FS.Data)]++
		totalPlayers := mapPlayerTypes(data, summary.PlayerTypes)
		summary.Players[fmt.Sprintf("%d", totalPlayers)]++

		// Bin tracks, albums, and artists
		mapToBins(data.Library.Tracks, TrackBins, summary.Tracks)
		mapToBins(data.Library.Albums, AlbumBins, summary.Albums)
		mapToBins(data.Library.Artists, ArtistBins, summary.Artists)

		// Collect values for statistics (only non-zero for tracks, albums, artists)
		if data.Library.Tracks > 0 {
			trackValues = append(trackValues, data.Library.Tracks)
		}
		if data.Library.Albums > 0 {
			albumValues = append(albumValues, data.Library.Albums)
		}
		if data.Library.Artists > 0 {
			artistValues = append(artistValues, data.Library.Artists)
		}
		// Collect all values for playlists, shares, radios, libraries (including zeros)
		playlistValues = append(playlistValues, data.Library.Playlists)
		shareValues = append(shareValues, data.Library.Shares)
		radioValues = append(radioValues, data.Library.Radios)
		libraryValues = append(libraryValues, data.Library.Libraries)
	}

	if summary.NumInstances == 0 {
		log.Printf("No data to summarize for %s", date.Format("2006-01-02"))
		return nil
	}

	// Calculate statistics for all fields
	summary.TrackStats = calcStats(trackValues)
	summary.AlbumStats = calcStats(albumValues)
	summary.ArtistStats = calcStats(artistValues)
	summary.PlaylistStats = calcStats(playlistValues)
	summary.ShareStats = calcStats(shareValues)
	summary.RadioStats = calcStats(radioValues)
	summary.LibraryStats = calcStats(libraryValues)

	// Save summary to file
	err = SaveSummary(summary, date)
	if err != nil {
		log.Printf("Error saving summary: %s", err)
	}
	return err
}

// calcStats computes min, max, mean, median, and standard deviation for a slice of values
func calcStats(values []int64) *Stats {
	if len(values) == 0 {
		return nil
	}

	// Sort for median calculation
	sorted := make([]int64, len(values))
	copy(sorted, values)
	slices.Sort(sorted)

	n := len(sorted)
	minVal := sorted[0]
	maxVal := sorted[n-1]

	// Calculate mean
	var sum int64
	for _, v := range sorted {
		sum += v
	}
	mean := float64(sum) / float64(n)

	// Calculate median
	var median float64
	if n%2 == 0 {
		median = float64(sorted[n/2-1]+sorted[n/2]) / 2
	} else {
		median = float64(sorted[n/2])
	}

	// Calculate standard deviation
	var sumSquaredDiff float64
	for _, v := range sorted {
		diff := float64(v) - mean
		sumSquaredDiff += diff * diff
	}
	stdDev := math.Sqrt(sumSquaredDiff / float64(n))

	return &Stats{
		Min:    minVal,
		Max:    maxVal,
		Mean:   mean,
		Median: median,
		StdDev: stdDev,
	}
}

// Match the first 8 characters of a git sha
var versionRegex = regexp.MustCompile(`\(([0-9a-fA-F]{8})[0-9a-fA-F]*\)`)

func mapVersion(data insights.Data) string {
	return versionRegex.ReplaceAllString(data.Version, "($1)")
}

var TrackBins = []int64{0, 1, 100, 500, 1000, 5000, 10000, 20000, 50000, 100000, 500000, 1000000}
var AlbumBins = []int64{0, 1, 10, 50, 100, 500, 1000, 2000, 5000, 10000, 50000, 100000}
var ArtistBins = []int64{0, 1, 10, 50, 100, 500, 1000, 2000, 5000, 10000, 50000, 100000}

func mapToBins(count int64, bins []int64, counters map[string]uint64) {
	for i := range bins {
		bin := bins[len(bins)-1-i]
		if count >= bin {
			counters[fmt.Sprintf("%d", bin)]++
			return
		}
	}
}

var caser = cases.Title(language.Und)

func mapOS(data insights.Data) string {
	osName := func() string {
		switch data.OS.Type {
		case "darwin":
			return "macOS"
		case "linux":
			if data.OS.Containerized {
				return "Linux (containerized)"
			}
			return "Linux"
		default:
			s := caser.String(data.OS.Type)
			return strings.Replace(s, "bsd", "BSD", -1)
		}
	}()
	return osName + " - " + data.OS.Arch
}

var playersTypes = map[*regexp.Regexp]string{
	regexp.MustCompile("NavidromeUI.*"):       "NavidromeUI",
	regexp.MustCompile("supersonic"):          "Supersonic",
	regexp.MustCompile("feishin"):             "", // Discard (old version reporting multiple times)
	regexp.MustCompile("audioling"):           "Audioling",
	regexp.MustCompile("^AginMusic.*"):        "AginMusic",
	regexp.MustCompile("playSub.*"):           "play:Sub",
	regexp.MustCompile("eu.callcc.audrey"):    "audrey",
	regexp.MustCompile("DSubCC"):              "", // Discard (chromecast)
	regexp.MustCompile(`bonob\+.*`):           "", // Discard (transcodings)
	regexp.MustCompile("https?://airsonic.*"): "Airsonic Refix",
	regexp.MustCompile("multi-scrobbler.*"):   "Multi-Scrobbler",
	regexp.MustCompile("SubMusic.*"):          "SubMusic",
	regexp.MustCompile("(?i)(hiby|_hiby_)"):   "HiBy",
	regexp.MustCompile("microSub"):            "AVSub",
	regexp.MustCompile("Stream Music"):        "Musiver",
}

func mapPlayerTypes(data insights.Data, players map[string]uint64) int64 {
	seen := map[string]uint64{}
	for p, count := range data.Library.ActivePlayers {
		for r, t := range playersTypes {
			if r.MatchString(p) {
				p = t
				break
			}
		}
		if p != "" {
			v := seen[p]
			seen[p] = max(v, uint64(count))
		}
	}
	var total int64
	for k, v := range seen {
		total += int64(v)
		players[k] += v
	}
	return total
}

var fsMappings = map[string]string{
	"unknown(0x2011bab0)": "exfat",
	"unknown(0x7366746e)": "ntfs",
	"unknown(0xc36400)":   "ceph",
	"unknown(0xf15f)":     "ecryptfs",
	"unknown(0xff534d42)": "cifs",
	"unknown(0x786f4256)": "vboxsf",
	"unknown(0xf2f52010)": "f2fs",
	"unknown(0x5346544e)": "ntfs",     // NTFS_SB_MAGIC
	"unknown(0x482b)":     "hfs+",     // HFS Plus (Apple)
	"unknown(0xca451a4e)": "virtiofs", // VirtIO filesystem (VMs/containers)
	"unknown(0x187)":      "autofs",   // Automount filesystem
	// Signed/unsigned conversion issues (negative hex values converted to uint32)
	"unknown(0x-6edc97c2)": "btrfs", // 0x9123683e
	"unknown(0x-1acb2be)":  "smb2",  // 0xfe534d42
	"unknown(0x-acb2be)":   "cifs",  // 0xff534d42
	"unknown(0x-d0adff0)":  "f2fs",  // 0xf2f52010
}

func mapFS(fs *insights.FSInfo) string {
	if fs == nil {
		return "unknown"
	}
	if t, ok := fsMappings[fs.Type]; ok {
		return t
	}
	return strings.ToLower(fs.Type)
}
