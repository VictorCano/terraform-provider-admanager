package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// schemaToStruct maps each discovery schema the manifest covers to the
// internal/client Go struct that mirrors it. The manifest is meant to list a
// struct's top-level json tags exactly; this table is what lets the sync test
// find the right struct for each schema.
var schemaToStruct = map[string]string{
	"GoogleAdsAdmanagerV1__AdUnit":               "AdUnit",
	"GoogleAdsAdmanagerV1__Placement":            "Placement",
	"GoogleAdsAdmanagerV1__CustomTargetingKey":   "CustomTargetingKey",
	"GoogleAdsAdmanagerV1__CustomTargetingValue": "CustomTargetingValue",
	"GoogleAdsAdmanagerV1__Network":              "Network",
}

// TestManifestMatchesClientStructs is the automated guard the discovery canary
// otherwise lacks: it fails when mapped_fields.json drifts out of sync with the
// json tags of the internal/client structs it is derived from. Without it, a PR
// adding a field to one of these structs could silently leave it outside drift
// detection (the canary would keep passing on the stale, narrower field set).
func TestManifestMatchesClientStructs(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	clientDir := filepath.Join(repoRoot, "internal", "client")
	manifestPath := filepath.Join(filepath.Dir(thisFile), "mapped_fields.json")

	m, err := loadManifest(manifestPath)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}

	structTags := clientStructJSONTags(t, clientDir)

	// Every schema the manifest covers must be a known struct, and vice versa,
	// so a new mapped schema cannot be added without wiring up its struct here.
	for schema := range m {
		if _, ok := schemaToStruct[schema]; !ok {
			t.Errorf("manifest schema %q has no entry in schemaToStruct; add its internal/client struct", schema)
		}
	}
	for schema, structName := range schemaToStruct {
		manifestFields, ok := m[schema]
		if !ok {
			t.Errorf("schemaToStruct maps %q -> %s but the manifest has no such schema", schema, structName)
			continue
		}
		tags, ok := structTags[structName]
		if !ok {
			t.Errorf("struct %s (for schema %q) not found in %s", structName, schema, clientDir)
			continue
		}
		wantSorted := append([]string(nil), tags...)
		gotSorted := append([]string(nil), manifestFields...)
		sort.Strings(wantSorted)
		sort.Strings(gotSorted)
		if !reflect.DeepEqual(wantSorted, gotSorted) {
			t.Errorf("manifest[%q] out of sync with struct %s json tags.\n manifest: %v\n  struct: %v\nRegenerate mapped_fields.json from the struct's top-level json tags.",
				schema, structName, gotSorted, wantSorted)
		}
	}
}

// clientStructJSONTags parses every non-test .go file in dir and returns, per
// top-level struct type, the json tag names of its direct fields (skipping
// untagged and json:"-" fields). It reads source rather than importing the
// package so the dependency-free canary tool stays decoupled from
// internal/client.
func clientStructJSONTags(t *testing.T, dir string) map[string][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	out := map[string][]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				out[ts.Name.Name] = jsonFieldNames(st)
			}
		}
	}
	return out
}

// jsonFieldNames extracts the json tag names of a struct's direct fields.
func jsonFieldNames(st *ast.StructType) []string {
	var names []string
	for _, field := range st.Fields.List {
		if field.Tag == nil {
			continue
		}
		raw, err := strconv.Unquote(field.Tag.Value)
		if err != nil {
			continue
		}
		name, _, _ := strings.Cut(reflect.StructTag(raw).Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names
}
