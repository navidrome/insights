package store

import (
	"testing"
	"time"

	"github.com/navidrome/navidrome/core/metrics/insights"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Store Suite")
}

// testDay is the fixed UTC day used across the suite.
var testDay = time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

// dataFor builds a minimal payload with the given instance ID.
func dataFor(id string) insights.Data {
	var d insights.Data
	d.InsightsID = id
	d.Version = "0.61.2"
	d.Library.Tracks = 100
	return d
}
