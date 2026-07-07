// Command discovery is a maintainer-protection canary. It fetches the public
// Ad Manager REST discovery document and diffs ONLY the fields the provider
// maps (schemas AdUnit, Placement, CustomTargetingKey, CustomTargetingValue and
// Network) against a checked-in snapshot. Drift within that mapped set — an
// added, removed, or type-changed field — is exactly the signal that the
// locked "schema mirrors the live discovery doc" decision was violated by an
// upstream Beta-API change.
//
// Whole-document diffs are deliberately out of scope: Google reshuffles the
// discovery JSON constantly and a full diff is pure noise.
//
// Exit codes are meaningful and distinct so a flaky fetch never masquerades as
// real drift:
//
//	0  no drift
//	1  drift detected within the mapped field set
//	2  operational error (bad flags, fetch/HTTP/parse failure, unreadable files)
//
// The workflow must only open a drift issue on exit code 1.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

const (
	exitOK    = 0
	exitDrift = 1
	exitError = 2

	defaultURL = "https://admanager.googleapis.com/$discovery/rest?version=v1"
)

// discoveryProperty is one property in a discovery schema. Only the shape the
// canary needs to compute a stable type string is decoded.
type discoveryProperty struct {
	Type  string             `json:"type"`
	Ref   string             `json:"$ref"`
	Items *discoveryProperty `json:"items"`
}

type discoverySchema struct {
	Properties map[string]discoveryProperty `json:"properties"`
}

type discoveryDoc struct {
	Schemas map[string]discoverySchema `json:"schemas"`
}

// typeString renders a property as a compact, comparable type token:
//
//	scalar  -> "string", "boolean", ...
//	$ref    -> the referenced schema name
//	array   -> "array<elem>" where elem is recursively rendered
//
// Arrays and refs are collapsed to their shape (not their full sub-schema) on
// purpose: the mapped set is the top-level fields of five schemas, and a change
// from e.g. string to boolean, or scalar to array, is the drift worth catching.
func typeString(p discoveryProperty) string {
	switch {
	case p.Ref != "":
		return p.Ref
	case p.Type == "array":
		elem := ""
		if p.Items != nil {
			elem = typeString(*p.Items)
		}
		return "array<" + elem + ">"
	default:
		return p.Type
	}
}

// manifest maps a discovery schema name to the mapped field names the provider
// cares about. It is a checked-in artifact derived from the json tags of the
// internal/client structs.
type manifest map[string][]string

// snapshot maps a schema name to field name to type string. It is the diff
// baseline and the shape produced by buildCurrent.
type snapshot map[string]map[string]string

// fprintf is fmt.Fprintf with the never-actionable io.Writer error dropped, so
// the diagnostic writes below stay one-liners and satisfy errcheck.
func fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

func loadManifest(path string) (manifest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a CI-controlled flag, not user input.
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest %s: %w", path, err)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("manifest %s is empty", path)
	}
	return m, nil
}

func loadSnapshot(path string) (snapshot, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a CI-controlled flag, not user input.
	if err != nil {
		return nil, err
	}
	var s snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing snapshot %s: %w", path, err)
	}
	return s, nil
}

// fetchDiscovery retrieves and parses the discovery document. Any transport,
// HTTP-status, or JSON error is returned so main can map it to the operational
// exit code rather than the drift exit code.
func fetchDiscovery(client *http.Client, url string) (*discoveryDoc, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching discovery doc: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading discovery doc: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery doc returned HTTP %d", resp.StatusCode)
	}
	var doc discoveryDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parsing discovery doc: %w", err)
	}
	if len(doc.Schemas) == 0 {
		return nil, fmt.Errorf("discovery doc has no schemas (malformed)")
	}
	return &doc, nil
}

// buildCurrent resolves, for every mapped field, its current discovery type. A
// mapped field absent from the live doc is simply omitted, which the diff reads
// as "removed".
func buildCurrent(doc *discoveryDoc, m manifest) snapshot {
	cur := snapshot{}
	for schemaName, fields := range m {
		sch, ok := doc.Schemas[schemaName]
		if !ok {
			continue
		}
		for _, field := range fields {
			prop, ok := sch.Properties[field]
			if !ok {
				continue
			}
			if cur[schemaName] == nil {
				cur[schemaName] = map[string]string{}
			}
			cur[schemaName][field] = typeString(prop)
		}
	}
	return cur
}

// diff compares the baseline snapshot against the freshly built one, scanning
// only the mapped field set defined by m. It returns a sorted, human-readable
// list of drift lines; empty means no drift.
func diff(base, cur snapshot, m manifest) []string {
	var lines []string
	schemas := make([]string, 0, len(m))
	for s := range m {
		schemas = append(schemas, s)
	}
	sort.Strings(schemas)
	for _, schema := range schemas {
		fields := append([]string(nil), m[schema]...)
		sort.Strings(fields)
		for _, field := range fields {
			was, hadBase := base[schema][field]
			now, hadCur := cur[schema][field]
			switch {
			case hadBase && !hadCur:
				lines = append(lines, fmt.Sprintf("REMOVED  %s.%s (was %s)", schema, field, was))
			case !hadBase && hadCur:
				lines = append(lines, fmt.Sprintf("ADDED    %s.%s (now %s)", schema, field, now))
			case hadBase && hadCur && was != now:
				lines = append(lines, fmt.Sprintf("CHANGED  %s.%s (%s -> %s)", schema, field, was, now))
			}
		}
	}
	return lines
}

type config struct {
	url          string
	manifestPath string
	snapshotPath string
	update       bool
	client       *http.Client
}

func run(cfg config, stdout, stderr io.Writer) int {
	m, err := loadManifest(cfg.manifestPath)
	if err != nil {
		fprintf(stderr, "discovery: %v\n", err)
		return exitError
	}

	doc, err := fetchDiscovery(cfg.client, cfg.url)
	if err != nil {
		fprintf(stderr, "discovery: %v\n", err)
		return exitError
	}

	cur := buildCurrent(doc, m)

	if cfg.update {
		if err := writeSnapshot(cfg.snapshotPath, cur); err != nil {
			fprintf(stderr, "discovery: %v\n", err)
			return exitError
		}
		fprintf(stdout, "discovery: wrote snapshot %s\n", cfg.snapshotPath)
		return exitOK
	}

	base, err := loadSnapshot(cfg.snapshotPath)
	if err != nil {
		fprintf(stderr, "discovery: %v\n", err)
		return exitError
	}

	drift := diff(base, cur, m)
	if len(drift) > 0 {
		fprintf(stderr, "discovery: mapped-field drift detected (%d):\n", len(drift))
		for _, line := range drift {
			fprintf(stderr, "  %s\n", line)
		}
		fprintf(stderr, "If the change is intentional, regenerate the snapshot with "+
			"`go run ./tools/canary/discovery -update` and review the diff; "+
			"otherwise the provider's schema mapping must be updated to match the live discovery doc.\n")
		return exitDrift
	}

	fprintf(stdout, "discovery: no drift across %d mapped fields; OK\n", countFields(m))
	return exitOK
}

func countFields(m manifest) int {
	n := 0
	for _, f := range m {
		n += len(f)
	}
	return n
}

// writeSnapshot serializes cur as stable, indented JSON (Go marshals map keys
// sorted) so the checked-in snapshot diffs cleanly in review. HTML escaping is
// disabled so array<...> type tokens stay readable.
func writeSnapshot(path string, s snapshot) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func main() {
	url := flag.String("url", defaultURL, "discovery document URL")
	manifestPath := flag.String("manifest", "tools/canary/discovery/mapped_fields.json", "path to the mapped-field manifest")
	snapshotPath := flag.String("snapshot", "tools/canary/discovery/snapshot.json", "path to the checked-in snapshot")
	update := flag.Bool("update", false, "overwrite the snapshot with the current mapped-field types and exit 0")
	timeout := flag.Duration("timeout", 60*time.Second, "HTTP timeout for the discovery fetch")
	flag.Parse()

	cfg := config{
		url:          *url,
		manifestPath: *manifestPath,
		snapshotPath: *snapshotPath,
		update:       *update,
		client:       &http.Client{Timeout: *timeout},
	}
	os.Exit(run(cfg, os.Stdout, os.Stderr))
}
