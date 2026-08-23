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

	log.Printf("Generating charts.json under %s", dataFolder) //#nosec G706 -- dataFolder is from controlled env var
	if err := charts.ExportChartsJSON(dataFolder); err != nil {
		log.Fatalf("Error exporting charts JSON: %v", err)
	}
	log.Print("Charts JSON generated successfully")
}
