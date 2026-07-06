package client

// All tests run against local httptest servers with a static fake token.
// No real Google credentials are ever used or required here.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreatePlacementPostsCorrectPathAndBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/placements" {
			t.Errorf("path = %q, want /v1/networks/123456/placements", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/placements/789",
			"placementId": "789",
			"displayName": "Homepage Bundle",
			"description": "All homepage inventory",
			"targetedAdUnits": ["networks/123456/adUnits/456"],
			"placementCode": "abc123",
			"status": "ACTIVE",
			"updateTime": "2026-07-05T12:00:00Z"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	in := &Placement{
		DisplayName:     "Homepage Bundle",
		Description:     "All homepage inventory",
		TargetedAdUnits: []string{"networks/123456/adUnits/456"},
	}
	out, err := c.CreatePlacement(context.Background(), in)
	if err != nil {
		t.Fatalf("CreatePlacement: %v", err)
	}
	if gotBody["displayName"] != "Homepage Bundle" {
		t.Errorf("request displayName = %v", gotBody["displayName"])
	}
	if _, ok := gotBody["targetedAdUnits"]; !ok {
		t.Errorf("request body missing targetedAdUnits: %v", gotBody)
	}
	// Output-only fields must not be sent on create.
	for _, k := range []string{"status", "placementCode", "updateTime", "placementId"} {
		if _, ok := gotBody[k]; ok {
			t.Errorf("request body must not carry output-only %s: %v", k, gotBody)
		}
	}
	if out.Name != "networks/123456/placements/789" || out.PlacementID != "789" ||
		out.PlacementCode != "abc123" || out.Status != "ACTIVE" {
		t.Errorf("unexpected decoded placement: %+v", out)
	}
	if len(out.TargetedAdUnits) != 1 || out.TargetedAdUnits[0] != "networks/123456/adUnits/456" {
		t.Errorf("unexpected decoded targetedAdUnits: %+v", out.TargetedAdUnits)
	}
}

func TestGetPlacementDecodesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/placements/789" {
			t.Errorf("path = %q, want /v1/networks/123456/placements/789", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/placements/789",
			"placementId": "789",
			"displayName": "Homepage Bundle",
			"placementCode": "abc123",
			"status": "ACTIVE",
			"updateTime": "2026-07-05T12:00:00Z"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	out, err := c.GetPlacement(context.Background(), "networks/123456/placements/789")
	if err != nil {
		t.Fatalf("GetPlacement: %v", err)
	}
	if out.PlacementCode != "abc123" || out.Status != "ACTIVE" ||
		out.UpdateTime != "2026-07-05T12:00:00Z" {
		t.Errorf("unexpected decoded placement: %+v", out)
	}
}

func TestGetPlacementNotFoundIsDistinguishable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Placement not found","status":"NOT_FOUND"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	_, err := c.GetPlacement(context.Background(), "networks/123456/placements/999")
	if err == nil {
		t.Fatal("expected error for missing placement, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true for a 404; err = %v", err)
	}
}

func TestPatchPlacementSendsUpdateMask(t *testing.T) {
	var gotBody map[string]any
	var gotMask string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/placements/789" {
			t.Errorf("path = %q, want /v1/networks/123456/placements/789", r.URL.Path)
		}
		gotMask = r.URL.Query().Get("updateMask")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/placements/789",
			"displayName": "New Name",
			"description": "New description"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	in := &Placement{
		Name:        "networks/123456/placements/789",
		DisplayName: "New Name",
		Description: "New description",
	}
	out, err := c.PatchPlacement(context.Background(), in, []string{"displayName", "description"})
	if err != nil {
		t.Fatalf("PatchPlacement: %v", err)
	}
	if gotMask != "displayName,description" {
		t.Errorf("updateMask = %q, want displayName,description", gotMask)
	}
	if gotBody["displayName"] != "New Name" || gotBody["description"] != "New description" {
		t.Errorf("unexpected patch body: %v", gotBody)
	}
	if out.DisplayName != "New Name" {
		t.Errorf("decoded displayName = %q", out.DisplayName)
	}
}

func TestPatchPlacementRejectsEmptyMask(t *testing.T) {
	c := testClient(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), Config{})
	if _, err := c.PatchPlacement(context.Background(), &Placement{Name: "networks/123456/placements/789"}, nil); err == nil {
		t.Fatal("expected an error for an empty update mask, got nil")
	}
}

func TestArchivePlacementPostsBatchBody(t *testing.T) {
	var gotBody struct {
		Names []string `json:"names"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/placements:batchArchive" {
			t.Errorf("path = %q, want /v1/networks/123456/placements:batchArchive", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	if err := c.ArchivePlacement(context.Background(), "networks/123456/placements/789"); err != nil {
		t.Fatalf("ArchivePlacement: %v", err)
	}
	if len(gotBody.Names) != 1 || gotBody.Names[0] != "networks/123456/placements/789" {
		t.Errorf("batch archive names = %v, want exactly [networks/123456/placements/789]", gotBody.Names)
	}
}
