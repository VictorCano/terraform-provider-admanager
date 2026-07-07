// Command soapversion is a maintainer-protection canary. It parses the SOAP
// shim version constant out of internal/soap/soap.go and fails when that
// version is older than a configurable window, because GAM SOAP versions sunset
// roughly 12 months after release on a rolling quarterly schedule and a stale
// constant silently breaks custom-targeting-value writes for every user.
//
// It is intentionally dependency-free (standard library only) so it can run in
// a bare CI job. The parsing and date math are split into small pure functions
// so they can be unit-tested without touching the filesystem or the clock.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"
)

// soapVersionRE matches the single source-of-truth declaration in
// internal/soap/soap.go: `const soapVersion = "v202605"`. The two capture
// groups are the four-digit year and two-digit month.
var soapVersionRE = regexp.MustCompile(`soapVersion\s*=\s*"v(\d{4})(\d{2})"`)

// parseSoapVersion extracts the release year and month from Go source that
// declares the soapVersion constant. It errors when the constant is absent or
// its value is not a well-formed vYYYYMM string.
func parseSoapVersion(src []byte) (year, month int, err error) {
	m := soapVersionRE.FindSubmatch(src)
	if m == nil {
		return 0, 0, fmt.Errorf(`could not find a soapVersion = "vYYYYMM" constant in source`)
	}
	year, _ = strconv.Atoi(string(m[1]))
	month, _ = strconv.Atoi(string(m[2]))
	if month < 1 || month > 12 {
		return 0, 0, fmt.Errorf("soapVersion month %02d is out of range (v%s%s)", month, m[1], m[2])
	}
	return year, month, nil
}

// parseNow parses a YYYY-MM string into a time anchored at the first of that
// month (UTC). It backs the injectable -now flag so tests are deterministic.
func parseNow(s string) (time.Time, error) {
	return time.Parse("2006-01", s)
}

// ageMonths returns how many whole calendar months separate the release
// (year/month) from now. It is negative when now precedes the release.
func ageMonths(releaseYear, releaseMonth int, now time.Time) int {
	return (now.Year()-releaseYear)*12 + (int(now.Month()) - releaseMonth)
}

type result struct {
	year      int
	month     int
	ageMonths int
	stale     bool
}

// evaluate parses the version out of src and decides whether it is stale
// relative to now. A version is stale once its age strictly exceeds warnMonths,
// so warnMonths=9 tolerates an age of exactly 9 and flags 10 or more.
func evaluate(src []byte, now time.Time, warnMonths int) (result, error) {
	y, m, err := parseSoapVersion(src)
	if err != nil {
		return result{}, err
	}
	age := ageMonths(y, m, now)
	return result{year: y, month: m, ageMonths: age, stale: age > warnMonths}, nil
}

func main() {
	file := flag.String("file", "internal/soap/soap.go", "path to the Go source declaring the soapVersion constant")
	nowFlag := flag.String("now", "", "reference month as YYYY-MM (defaults to the current month)")
	warnMonths := flag.Int("warn-months", 9, "flag the version once it is older than this many months")
	flag.Parse()

	now := time.Now().UTC()
	if *nowFlag != "" {
		parsed, err := parseNow(*nowFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "soapversion: invalid -now %q: %v\n", *nowFlag, err)
			os.Exit(2)
		}
		now = parsed
	}

	src, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "soapversion: %v\n", err)
		os.Exit(2)
	}

	res, err := evaluate(src, now, *warnMonths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "soapversion: %v\n", err)
		os.Exit(2)
	}

	if res.stale {
		fmt.Fprintf(os.Stderr,
			"soapversion: SOAP shim is v%04d%02d, %d months old (> %d-month window). "+
				"GAM SOAP versions sunset ~12 months after release; bump soapVersion in "+
				"internal/soap/soap.go to the newest available version "+
				"(https://developers.google.com/ad-manager/api/deprecation) before custom "+
				"targeting value writes break.\n",
			res.year, res.month, res.ageMonths, *warnMonths)
		os.Exit(1)
	}

	fmt.Printf("soapversion: v%04d%02d is %d months old (within the %d-month window); OK\n",
		res.year, res.month, res.ageMonths, *warnMonths)
}
