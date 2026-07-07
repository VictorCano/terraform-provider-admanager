package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureManifest is the mapped-field manifest used by the synthetic tests: two
// schemas, a small closed field set so drift is easy to arrange precisely.
const fixtureManifest = `{
  "SchemaA": ["name", "count", "tags"],
  "SchemaB": ["title"]
}`

// discoveryDocJSON builds a discovery document that declares the given
// schema->property->rawType. rawType is a small DSL: "string"/"boolean" become
// scalars, "array<elem>" becomes an array, "ref:Name" becomes a $ref.
func discoveryDocJSON(t *testing.T, schemas map[string]map[string]string) string {
	t.Helper()
	type prop struct {
		Type  string `json:"type,omitempty"`
		Ref   string `json:"$ref,omitempty"`
		Items *struct {
			Type string `json:"type,omitempty"`
			Ref  string `json:"$ref,omitempty"`
		} `json:"items,omitempty"`
	}
	out := map[string]any{"schemas": map[string]any{}}
	sc := out["schemas"].(map[string]any)
	for name, props := range schemas {
		p := map[string]any{}
		for field, raw := range props {
			var pr prop
			switch {
			case strings.HasPrefix(raw, "array<"):
				elem := strings.TrimSuffix(strings.TrimPrefix(raw, "array<"), ">")
				pr.Type = "array"
				pr.Items = &struct {
					Type string `json:"type,omitempty"`
					Ref  string `json:"$ref,omitempty"`
				}{Type: elem}
			case strings.HasPrefix(raw, "ref:"):
				pr.Ref = strings.TrimPrefix(raw, "ref:")
			default:
				pr.Type = raw
			}
			p[field] = pr
		}
		sc[name] = map[string]any{"properties": p}
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal fixture doc: %v", err)
	}
	return string(data)
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// serve returns an httptest server that responds with status/body.
func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// harness wires the fixture manifest, a snapshot, and a served discovery doc
// into a run() invocation and returns the exit code plus combined output.
func harness(t *testing.T, snapshotJSON, servedDoc string, status int) (int, string) {
	t.Helper()
	dir := t.TempDir()
	manifestPath := writeFile(t, dir, "mapped_fields.json", fixtureManifest)
	snapshotPath := writeFile(t, dir, "snapshot.json", snapshotJSON)
	srv := serve(t, status, servedDoc)

	var stdout, stderr bytes.Buffer
	code := run(config{
		url:          srv.URL,
		manifestPath: manifestPath,
		snapshotPath: snapshotPath,
		client:       srv.Client(),
	}, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

// baselineDoc / baselineSnapshot describe the mapped set with zero drift.
func baselineDoc(t *testing.T) string {
	return discoveryDocJSON(t, map[string]map[string]string{
		"SchemaA": {"name": "string", "count": "boolean", "tags": "array<string>"},
		"SchemaB": {"title": "string"},
	})
}

const baselineSnapshot = `{
  "SchemaA": {"name": "string", "count": "boolean", "tags": "array<string>"},
  "SchemaB": {"title": "string"}
}`

func TestRunNoDrift(t *testing.T) {
	code, out := harness(t, baselineSnapshot, baselineDoc(t), http.StatusOK)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d; output:\n%s", code, exitOK, out)
	}
	if !strings.Contains(out, "no drift") {
		t.Fatalf("expected no-drift message, got:\n%s", out)
	}
}

func TestRunAddedField(t *testing.T) {
	// Snapshot omits SchemaA.tags; the live doc has it -> ADDED.
	snap := `{
      "SchemaA": {"name": "string", "count": "boolean"},
      "SchemaB": {"title": "string"}
    }`
	code, out := harness(t, snap, baselineDoc(t), http.StatusOK)
	if code != exitDrift {
		t.Fatalf("exit = %d, want %d; output:\n%s", code, exitDrift, out)
	}
	if !strings.Contains(out, "ADDED") || !strings.Contains(out, "SchemaA.tags") {
		t.Fatalf("expected ADDED SchemaA.tags, got:\n%s", out)
	}
}

func TestRunRemovedField(t *testing.T) {
	// Live doc drops SchemaA.count -> REMOVED.
	doc := discoveryDocJSON(t, map[string]map[string]string{
		"SchemaA": {"name": "string", "tags": "array<string>"},
		"SchemaB": {"title": "string"},
	})
	code, out := harness(t, baselineSnapshot, doc, http.StatusOK)
	if code != exitDrift {
		t.Fatalf("exit = %d, want %d; output:\n%s", code, exitDrift, out)
	}
	if !strings.Contains(out, "REMOVED") || !strings.Contains(out, "SchemaA.count") {
		t.Fatalf("expected REMOVED SchemaA.count, got:\n%s", out)
	}
}

func TestRunTypeChange(t *testing.T) {
	// Live doc changes SchemaA.count from boolean to string -> CHANGED.
	doc := discoveryDocJSON(t, map[string]map[string]string{
		"SchemaA": {"name": "string", "count": "string", "tags": "array<string>"},
		"SchemaB": {"title": "string"},
	})
	code, out := harness(t, baselineSnapshot, doc, http.StatusOK)
	if code != exitDrift {
		t.Fatalf("exit = %d, want %d; output:\n%s", code, exitDrift, out)
	}
	if !strings.Contains(out, "CHANGED") || !strings.Contains(out, "boolean -> string") {
		t.Fatalf("expected CHANGED count boolean -> string, got:\n%s", out)
	}
}

func TestRunMalformedDocIsOperationalError(t *testing.T) {
	code, out := harness(t, baselineSnapshot, "{not valid json", http.StatusOK)
	if code != exitError {
		t.Fatalf("exit = %d, want %d (operational); output:\n%s", code, exitError, out)
	}
	if strings.Contains(out, "drift") {
		t.Fatalf("a parse failure must not be reported as drift, got:\n%s", out)
	}
}

func TestRunHTTP500IsOperationalError(t *testing.T) {
	// The body is a valid discovery doc; only the status is bad, so this
	// exercises the status-code guard specifically (not the JSON parser).
	code, out := harness(t, baselineSnapshot, baselineDoc(t), http.StatusInternalServerError)
	if code != exitError {
		t.Fatalf("exit = %d, want %d (operational); output:\n%s", code, exitError, out)
	}
	if strings.Contains(out, "drift") {
		t.Fatalf("an HTTP 500 must not be reported as drift, got:\n%s", out)
	}
}

func TestRunEmptySchemasIsOperationalError(t *testing.T) {
	code, _ := harness(t, baselineSnapshot, `{"schemas": {}}`, http.StatusOK)
	if code != exitError {
		t.Fatalf("exit = %d, want %d for a schemas-less doc", code, exitError)
	}
}

func TestRunMissingManifestIsOperationalError(t *testing.T) {
	dir := t.TempDir()
	snapshotPath := writeFile(t, dir, "snapshot.json", baselineSnapshot)
	srv := serve(t, http.StatusOK, baselineDoc(t))
	var out bytes.Buffer
	code := run(config{
		url:          srv.URL,
		manifestPath: filepath.Join(dir, "does-not-exist.json"),
		snapshotPath: snapshotPath,
		client:       srv.Client(),
	}, &out, &out)
	if code != exitError {
		t.Fatalf("exit = %d, want %d for missing manifest", code, exitError)
	}
}

func TestRunUpdateWritesSnapshot(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeFile(t, dir, "mapped_fields.json", fixtureManifest)
	snapshotPath := filepath.Join(dir, "snapshot.json")
	srv := serve(t, http.StatusOK, baselineDoc(t))

	var out bytes.Buffer
	code := run(config{
		url:          srv.URL,
		manifestPath: manifestPath,
		snapshotPath: snapshotPath,
		update:       true,
		client:       srv.Client(),
	}, &out, &out)
	if code != exitOK {
		t.Fatalf("update exit = %d, want %d; %s", code, exitOK, out.String())
	}

	written, err := loadSnapshot(snapshotPath)
	if err != nil {
		t.Fatalf("load written snapshot: %v", err)
	}
	if written["SchemaA"]["tags"] != "array<string>" {
		t.Fatalf("written snapshot missing resolved type, got %v", written)
	}
	// A follow-up diff against the just-written snapshot must be clean.
	code2, out2 := harness(t, mustJSON(t, written), baselineDoc(t), http.StatusOK)
	if code2 != exitOK {
		t.Fatalf("re-diff after update = %d, want %d; %s", code2, exitOK, out2)
	}
}

func TestTypeString(t *testing.T) {
	cases := []struct {
		name string
		p    discoveryProperty
		want string
	}{
		{"scalar", discoveryProperty{Type: "string"}, "string"},
		{"ref", discoveryProperty{Ref: "GoogleAdsAdmanagerV1__Size"}, "GoogleAdsAdmanagerV1__Size"},
		{"array of scalar", discoveryProperty{Type: "array", Items: &discoveryProperty{Type: "string"}}, "array<string>"},
		{"array of ref", discoveryProperty{Type: "array", Items: &discoveryProperty{Ref: "X"}}, "array<X>"},
		{"array without items", discoveryProperty{Type: "array"}, "array<>"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := typeString(tt.p); got != tt.want {
				t.Fatalf("typeString = %q, want %q", got, tt.want)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}
