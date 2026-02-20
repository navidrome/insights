package main

import (
	"log"
	"os"

	"github.com/navidrome/insights/charts"
)

func main() {
	dataFolder := os.Getenv("DATA_FOLDER")
	if dataFolder == "" {
		dataFolder = "."
	}

	chartDataDir := dataFolder + "/web/chartdata"

	log.Printf("Generating charts.json in %s", chartDataDir) //#nosec G706 -- chartDataDir is from controlled env var
	if err := charts.ExportChartsJSON(chartDataDir); err != nil {
		log.Fatalf("Error exporting charts JSON: %v", err)
	}
	log.Print("Charts JSON generated successfully")
}
