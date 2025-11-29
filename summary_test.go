package main

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
	RunSpecs(t, "Insights Suite")
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
	)
})

type insightsOS struct {
	Type          string `json:"type"`
	Distro        string `json:"distro,omitempty"`
	Version       string `json:"version,omitempty"`
	Containerized bool   `json:"containerized"`
	Arch          string `json:"arch"`
	NumCPU        int    `json:"numCPU"`
}

type insightsLibrary struct {
	Tracks        int64            `json:"tracks"`
	Albums        int64            `json:"albums"`
	Artists       int64            `json:"artists"`
	Playlists     int64            `json:"playlists"`
	Shares        int64            `json:"shares"`
	Radios        int64            `json:"radios"`
	ActiveUsers   int64            `json:"activeUsers"`
	ActivePlayers map[string]int64 `json:"activePlayers,omitempty"`
}
