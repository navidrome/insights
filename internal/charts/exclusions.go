package charts

import (
	"bufio"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/navidrome/insights/internal/consts"
)

// loadPlayerExclusions reads the player exclusion patterns from the data folder: one regex per
// line, matched against the normalized player names the summaries store. Blank lines and lines
// starting with # are skipped.
//
// Nothing here is fatal. A missing file means no exclusions, and an unreadable file or a bad
// pattern is logged and skipped: a typo in a hand-edited file should not stop the charts.
func loadPlayerExclusions(dataFolder string) []*regexp.Regexp {
	path := filepath.Join(dataFolder, consts.PlayerExclusionsFile)
	f, err := os.Open(path) //#nosec G304 -- fixed file name inside the configured data folder
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		log.Printf("Warning: reading %s: %v; excluding no players", path, err)
		return nil
	}
	defer func() { _ = f.Close() }()

	var rules []*regexp.Regexp
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := regexp.Compile(line)
		if err != nil {
			log.Printf("Warning: %s line %d: %v; skipping it", path, n, err)
			continue
		}
		rules = append(rules, r)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Warning: reading %s: %v; using the %d patterns read before it", path, err, len(rules))
	}
	return rules
}

// logExclusions reports, per rule, what it takes out of one day's players. The file is edited by
// hand on the server, and a pattern that matches everything empties the player charts without
// any error, so these lines are the only place a bad rule shows up before someone sees the charts.
// A name two rules match counts under both.
func logExclusions(players map[string]uint64, rules []*regexp.Regexp, day time.Time) {
	var total uint64
	for _, c := range players {
		total += c
	}
	for _, r := range rules {
		var names int
		var removed uint64
		for name, c := range players {
			if r.MatchString(name) {
				names++
				removed += c
			}
		}
		log.Printf("Player exclusion %q removed %d names, %d of %d players on %s",
			r.String(), names, removed, total, day.Format(consts.DateFormat))
	}
}

// dropExcludedPlayers deletes from players every name that matches one of the rules.
func dropExcludedPlayers(players map[string]uint64, rules []*regexp.Regexp) {
	for name := range players {
		for _, r := range rules {
			if r.MatchString(name) {
				delete(players, name)
				break
			}
		}
	}
}
