package summary

import (
	"maps"
	"slices"
	"testing"

	"github.com/navidrome/navidrome/core/metrics/insights"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSummary(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Summary Suite")
}

var _ = Describe("Summary", func() {
	Describe("mapToBins", func() {
		var counters map[string]uint64
		var testBins = []int64{0, 1, 5, 10, 20, 50, 100, 200, 500, 1000}

		BeforeEach(func() {
			counters = make(map[string]uint64)
		})

		It("should map count to the correct bin", func() {
			mapToBins(0, testBins, counters)
			Expect(counters["0"]).To(Equal(uint64(1)))

			mapToBins(1, testBins, counters)
			Expect(counters["1"]).To(Equal(uint64(1)))

			mapToBins(10, testBins, counters)
			Expect(counters["10"]).To(Equal(uint64(1)))

			mapToBins(101, testBins, counters)
			Expect(counters["100"]).To(Equal(uint64(1)))

			mapToBins(1000, testBins, counters)
			Expect(counters["1000"]).To(Equal(uint64(1)))
		})

		It("should map count to the highest bin if count exceeds all bins", func() {
			mapToBins(2000, testBins, counters)
			Expect(counters["1000"]).To(Equal(uint64(1)))
		})

		It("should increment the correct bin count", func() {
			mapToBins(5, testBins, counters)
			mapToBins(5, testBins, counters)
			Expect(counters["5"]).To(Equal(uint64(2)))
		})

		It("should handle empty bins array", func() {
			mapToBins(5, []int64{}, counters)
			Expect(counters).To(BeEmpty())
		})
	})

	DescribeTable("mapVersion",
		func(expected string, data insights.Data) {
			Expect(mapVersion(data)).To(Equal(expected))
		},
		Entry("should map version", "0.54.2 (0b184893)", insights.Data{Version: "0.54.2 (0b184893)"}),
		Entry("should map version with long hash", "0.54.2 (0b184893)", insights.Data{Version: "0.54.2 (0b184893278620bb421a85c8b47df36900cd4df7)"}),
		Entry("should map version with no hash", "dev", insights.Data{Version: "dev"}),
		Entry("should map version with other values", "0.54.3 (source_archive)", insights.Data{Version: "0.54.3 (source_archive)"}),
		Entry("should map any version with a hash", "0.54.3-SNAPSHOT (734eb30a)", insights.Data{Version: "0.54.3-SNAPSHOT (734eb30a)"}),
	)

	DescribeTable("mapOS",
		func(expected string, data insights.Data) {
			Expect(mapOS(data)).To(Equal(expected))
		},
		Entry("should map darwin to macOS", "macOS - x86_64", insights.Data{OS: insightsOS{Type: "darwin", Arch: "x86_64"}}),
		Entry("should map linux to Linux", "Linux - x86_64", insights.Data{OS: insightsOS{Type: "linux", Arch: "x86_64"}}),
		Entry("should map containerized linux to Linux (containerized)", "Linux (containerized) - x86_64", insights.Data{OS: insightsOS{Type: "linux", Containerized: true, Arch: "x86_64"}}),
		Entry("should map bsd to BSD", "FreeBSD - x86_64", insights.Data{OS: insightsOS{Type: "freebsd", Arch: "x86_64"}}),
		Entry("should map unknown OS types", "Unknown - x86_64", insights.Data{OS: insightsOS{Type: "unknown", Arch: "x86_64"}}),
	)
	Describe("calcStats", func() {
		It("should return nil for empty slice", func() {
			Expect(calcStats([]int64{})).To(BeNil())
		})

		It("should calculate stats for a single value", func() {
			stats := calcStats([]int64{42})
			Expect(stats.Min).To(Equal(int64(42)))
			Expect(stats.Max).To(Equal(int64(42)))
			Expect(stats.Mean).To(Equal(float64(42)))
			Expect(stats.Median).To(Equal(float64(42)))
			Expect(stats.StdDev).To(Equal(float64(0)))
		})

		It("should calculate stats for odd number of values", func() {
			stats := calcStats([]int64{1, 2, 3, 4, 5})
			Expect(stats.Min).To(Equal(int64(1)))
			Expect(stats.Max).To(Equal(int64(5)))
			Expect(stats.Mean).To(Equal(float64(3)))
			Expect(stats.Median).To(Equal(float64(3)))
			Expect(stats.StdDev).To(BeNumerically("~", 1.414, 0.001))
		})

		It("should calculate stats for even number of values", func() {
			stats := calcStats([]int64{1, 2, 3, 4})
			Expect(stats.Min).To(Equal(int64(1)))
			Expect(stats.Max).To(Equal(int64(4)))
			Expect(stats.Mean).To(Equal(float64(2.5)))
			Expect(stats.Median).To(Equal(float64(2.5)))
			Expect(stats.StdDev).To(BeNumerically("~", 1.118, 0.001))
		})

		It("should handle unsorted input", func() {
			stats := calcStats([]int64{5, 1, 3, 2, 4})
			Expect(stats.Min).To(Equal(int64(1)))
			Expect(stats.Max).To(Equal(int64(5)))
			Expect(stats.Median).To(Equal(float64(3)))
		})

		It("should handle values with zeros", func() {
			stats := calcStats([]int64{0, 0, 10, 20})
			Expect(stats.Min).To(Equal(int64(0)))
			Expect(stats.Max).To(Equal(int64(20)))
			Expect(stats.Mean).To(Equal(float64(7.5)))
			Expect(stats.Median).To(Equal(float64(5)))
		})
	})

	Describe("mapFileSuffixes", func() {
		It("should count one instance per suffix", func() {
			suffixes := make(map[string]uint64)
			data := insights.Data{Library: insightsLibrary{FileSuffixes: map[string]int64{"mp3": 100, "flac": 50}}}
			mapFileSuffixes(data, suffixes)
			Expect(suffixes).To(Equal(map[string]uint64{"mp3": 1, "flac": 1}))
		})

		It("should count the number of instances that have each suffix", func() {
			suffixes := make(map[string]uint64)
			data1 := insights.Data{Library: insightsLibrary{FileSuffixes: map[string]int64{"mp3": 100, "flac": 50}}}
			data2 := insights.Data{Library: insightsLibrary{FileSuffixes: map[string]int64{"mp3": 200, "ogg": 30}}}
			mapFileSuffixes(data1, suffixes)
			mapFileSuffixes(data2, suffixes)
			Expect(suffixes).To(Equal(map[string]uint64{"mp3": 2, "flac": 1, "ogg": 1}))
		})

		It("should handle empty file suffixes", func() {
			suffixes := make(map[string]uint64)
			data := insights.Data{Library: insightsLibrary{}}
			mapFileSuffixes(data, suffixes)
			Expect(suffixes).To(BeEmpty())
		})
	})

	Describe("mapPlugins", func() {
		It("should count instances per plugin name and version", func() {
			plugins := make(map[string]uint64)
			versions := make(map[string]uint64)
			data := insights.Data{Plugins: map[string]insights.PluginInfo{
				"p1": {Name: "bonob", Version: "1.2.3"},
				"p2": {Name: "listenbrainz", Version: "0.5.0"},
			}}
			mapPlugins(data, plugins, versions)
			Expect(plugins).To(Equal(map[string]uint64{"bonob": 1, "listenbrainz": 1}))
			Expect(versions).To(Equal(map[string]uint64{"bonob/1.2.3": 1, "listenbrainz/0.5.0": 1}))
		})

		It("should accumulate across multiple instances", func() {
			plugins := make(map[string]uint64)
			versions := make(map[string]uint64)
			data1 := insights.Data{Plugins: map[string]insights.PluginInfo{
				"p1": {Name: "bonob", Version: "1.2.3"},
			}}
			data2 := insights.Data{Plugins: map[string]insights.PluginInfo{
				"p1": {Name: "bonob", Version: "1.3.0"},
			}}
			mapPlugins(data1, plugins, versions)
			mapPlugins(data2, plugins, versions)
			Expect(plugins).To(Equal(map[string]uint64{"bonob": 2}))
			Expect(versions).To(Equal(map[string]uint64{"bonob/1.2.3": 1, "bonob/1.3.0": 1}))
		})

		It("should handle no plugins", func() {
			plugins := make(map[string]uint64)
			versions := make(map[string]uint64)
			data := insights.Data{}
			mapPlugins(data, plugins, versions)
			Expect(plugins).To(BeEmpty())
			Expect(versions).To(BeEmpty())
		})
	})

	DescribeTable("mapPlayerTypes",
		func(data insights.Data, expected map[string]uint64) {
			players := make(map[string]uint64)
			c := mapPlayerTypes(data, players)
			Expect(players).To(Equal(expected))
			values := slices.Collect(maps.Values(expected))
			var total uint64
			for _, v := range values {
				total += v
			}
			Expect(c).To(Equal(int64(total)))
		},
		Entry("Feishin player", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"feishin_": 1, "Feishin": 1}}}, map[string]uint64{"Feishin": 1}),
		Entry("NavidromeUI player", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"NavidromeUI_1.0": 2}}}, map[string]uint64{"NavidromeUI": 2}),
		Entry("play:Sub player", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"playSub_iPhone11": 2, "playSub": 1}}}, map[string]uint64{"play:Sub": 2}),
		Entry("audrey player", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"eu.callcc.audrey": 4}}}, map[string]uint64{"audrey": 4}),
		Entry("discard DSubCC player", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"DSubCC": 5}}}, map[string]uint64{}),
		Entry("bonob player", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"bonob": 6, "bonob+ogg": 4}}}, map[string]uint64{"bonob": 6}),
		Entry("Airsonic Refix player", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"http://airsonic.netlify.app": 7}}}, map[string]uint64{"Airsonic Refix": 7}),
		Entry("Airsonic Refix player (HTTPS)", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"https://airsonic.netlify.app": 7}}}, map[string]uint64{"Airsonic Refix": 7}),
		Entry("Multiple players", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"Feishin": 1, "NavidromeUI_1.0": 2, "playSub_1.0": 3, "eu.callcc.audrey": 4, "DSubCC": 5, "bonob": 6, "bonob+ogg": 4, "http://airsonic.netlify.app": 7}}},
			map[string]uint64{"Feishin": 1, "NavidromeUI": 2, "play:Sub": 3, "audrey": 4, "bonob": 6, "Airsonic Refix": 7}),
		Entry("AudioMuse-AI player", insights.Data{Library: insightsLibrary{ActivePlayers: map[string]int64{"AudioMuse-AI/v0.8.9": 5}}}, map[string]uint64{"AudioMuse-AI": 5}),
	)

	Describe("mapConfigFlags", func() {
		It("should count true boolean fields using JSON tag names", func() {
			configFlags := make(map[string]uint64)
			data := insights.Data{Config: insightsConfig{
				ScannerEnabled: true,
				EnableLastFM:   true,
				TLSConfigured:  false,
			}}
			mapConfigFlags(data, configFlags)
			Expect(configFlags["scannerEnabled"]).To(Equal(uint64(1)))
			Expect(configFlags["enableLastFM"]).To(Equal(uint64(1)))
			Expect(configFlags).NotTo(HaveKey("tlsConfigured"))
		})

		It("should accumulate counts across multiple instances", func() {
			configFlags := make(map[string]uint64)
			data1 := insights.Data{Config: insightsConfig{ScannerEnabled: true, EnableLastFM: true}}
			data2 := insights.Data{Config: insightsConfig{ScannerEnabled: true, EnableLastFM: false}}
			mapConfigFlags(data1, configFlags)
			mapConfigFlags(data2, configFlags)
			Expect(configFlags["scannerEnabled"]).To(Equal(uint64(2)))
			Expect(configFlags["enableLastFM"]).To(Equal(uint64(1)))
		})

		It("should skip non-boolean fields", func() {
			configFlags := make(map[string]uint64)
			data := insights.Data{Config: insightsConfig{
				ScannerExtractor: "taglib",
				LogLevel:         "info",
				ScannerEnabled:   true,
			}}
			mapConfigFlags(data, configFlags)
			Expect(configFlags).NotTo(HaveKey("scannerExtractor"))
			Expect(configFlags).NotTo(HaveKey("logLevel"))
			Expect(configFlags["scannerEnabled"]).To(Equal(uint64(1)))
		})

		It("should handle all-false config", func() {
			configFlags := make(map[string]uint64)
			data := insights.Data{Config: insightsConfig{}}
			mapConfigFlags(data, configFlags)
			Expect(configFlags).To(BeEmpty())
		})
	})
})

type insightsConfig struct {
	LogLevel                string `json:"logLevel,omitempty"`
	LogFileConfigured       bool   `json:"logFileConfigured,omitempty"`
	TLSConfigured           bool   `json:"tlsConfigured,omitempty"`
	ScannerEnabled          bool   `json:"scannerEnabled,omitempty"`
	ScannerExtractor        string `json:"scannerExtractor,omitempty"`
	ScanSchedule            string `json:"scanSchedule,omitempty"`
	ScanWatcherWait         uint64 `json:"scanWatcherWait,omitempty"`
	ScanOnStartup           bool   `json:"scanOnStartup,omitempty"`
	TranscodingCacheSize    string `json:"transcodingCacheSize,omitempty"`
	ImageCacheSize          string `json:"imageCacheSize,omitempty"`
	EnableArtworkPrecache   bool   `json:"enableArtworkPrecache,omitempty"`
	EnableDownloads         bool   `json:"enableDownloads,omitempty"`
	EnableSharing           bool   `json:"enableSharing,omitempty"`
	EnableStarRating        bool   `json:"enableStarRating,omitempty"`
	EnableLastFM            bool   `json:"enableLastFM,omitempty"`
	EnableListenBrainz      bool   `json:"enableListenBrainz,omitempty"`
	EnableDeezer            bool   `json:"enableDeezer,omitempty"`
	EnableMediaFileCoverArt bool   `json:"enableMediaFileCoverArt,omitempty"`
	EnableSpotify           bool   `json:"enableSpotify,omitempty"`
	EnableJukebox           bool   `json:"enableJukebox,omitempty"`
	EnablePrometheus        bool   `json:"enablePrometheus,omitempty"`
	EnableCoverAnimation    bool   `json:"enableCoverAnimation,omitempty"`
	EnableNowPlaying        bool   `json:"enableNowPlaying,omitempty"`
	SessionTimeout          uint64 `json:"sessionTimeout,omitempty"`
	SearchFullString        bool   `json:"searchFullString,omitempty"`
	RecentlyAddedByModTime  bool   `json:"recentlyAddedByModTime,omitempty"`
	PreferSortTags          bool   `json:"preferSortTags,omitempty"`
	BackupSchedule          string `json:"backupSchedule,omitempty"`
	BackupCount             int    `json:"backupCount,omitempty"`
	DevActivityPanel        bool   `json:"devActivityPanel,omitempty"`
	DefaultBackgroundURLSet bool   `json:"defaultBackgroundURL,omitempty"`
	HasSmartPlaylists       bool   `json:"hasSmartPlaylists,omitempty"`
	ReverseProxyConfigured  bool   `json:"reverseProxyConfigured,omitempty"`
	HasCustomPID            bool   `json:"hasCustomPID,omitempty"`
	HasCustomTags           bool   `json:"hasCustomTags,omitempty"`
}

type insightsOS struct {
	Type          string `json:"type"`
	Distro        string `json:"distro,omitempty"`
	Version       string `json:"version,omitempty"`
	Containerized bool   `json:"containerized"`
	Arch          string `json:"arch"`
	NumCPU        int    `json:"numCPU"`
	Package       string `json:"package,omitempty"`
}

type insightsLibrary struct {
	Tracks        int64            `json:"tracks"`
	Albums        int64            `json:"albums"`
	Artists       int64            `json:"artists"`
	Playlists     int64            `json:"playlists"`
	Shares        int64            `json:"shares"`
	Radios        int64            `json:"radios"`
	Libraries     int64            `json:"libraries"`
	ActiveUsers   int64            `json:"activeUsers"`
	ActivePlayers map[string]int64 `json:"activePlayers,omitempty"`
	FileSuffixes  map[string]int64 `json:"fileSuffixes,omitempty"`
}
