package client

// All tests run against local httptest servers with a static fake token.
// No real Google credentials are ever used or required here.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCreateAdUnitPostsCorrectPathAndBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/adUnits" {
			t.Errorf("path = %q, want /v1/networks/123456/adUnits", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/adUnits/456",
			"adUnitId": "456",
			"displayName": "Homepage Leaderboard",
			"parentAdUnit": "networks/123456/adUnits/1",
			"adUnitCode": "homepage_leaderboard",
			"status": "ACTIVE",
			"adUnitSizes": [{"size": {"width": 728, "height": 90, "sizeType": "PIXEL"}, "environmentType": "BROWSER"}]
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	in := &AdUnit{
		DisplayName:  "Homepage Leaderboard",
		ParentAdUnit: "networks/123456/adUnits/1",
		AdUnitSizes: []AdUnitSize{
			{Size: Size{Width: 728, Height: 90, SizeType: "PIXEL"}, EnvironmentType: "BROWSER"},
		},
	}
	out, err := c.CreateAdUnit(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateAdUnit: %v", err)
	}
	if gotBody["displayName"] != "Homepage Leaderboard" {
		t.Errorf("request displayName = %v", gotBody["displayName"])
	}
	if gotBody["parentAdUnit"] != "networks/123456/adUnits/1" {
		t.Errorf("request parentAdUnit = %v", gotBody["parentAdUnit"])
	}
	if _, ok := gotBody["adUnitSizes"]; !ok {
		t.Errorf("request body missing adUnitSizes: %v", gotBody)
	}
	// Output-only fields must not be sent on create.
	if _, ok := gotBody["status"]; ok {
		t.Errorf("request body must not carry output-only status: %v", gotBody)
	}
	if out.Name != "networks/123456/adUnits/456" || out.AdUnitID != "456" ||
		out.AdUnitCode != "homepage_leaderboard" || out.Status != "ACTIVE" {
		t.Errorf("unexpected decoded ad unit: %+v", out)
	}
	if len(out.AdUnitSizes) != 1 || out.AdUnitSizes[0].Size.Width != 728 {
		t.Errorf("unexpected decoded sizes: %+v", out.AdUnitSizes)
	}
}

func TestGetAdUnitDecodesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/adUnits/456" {
			t.Errorf("path = %q, want /v1/networks/123456/adUnits/456", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/adUnits/456",
			"adUnitId": "456",
			"displayName": "Homepage Leaderboard",
			"parentAdUnit": "networks/123456/adUnits/1",
			"effectiveTargetWindow": "TOP",
			"effectiveAdsenseEnabled": true,
			"hasChildren": false,
			"status": "ACTIVE",
			"updateTime": "2026-07-05T12:00:00Z"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	out, err := c.GetAdUnit(context.Background(), "networks/123456/adUnits/456")
	if err != nil {
		t.Fatalf("GetAdUnit: %v", err)
	}
	if out.EffectiveTargetWindow != "TOP" || !out.EffectiveAdsenseEnabled ||
		out.UpdateTime != "2026-07-05T12:00:00Z" {
		t.Errorf("unexpected decoded ad unit: %+v", out)
	}
}

func TestGetAdUnitNotFoundIsDistinguishable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"AdUnit not found","status":"NOT_FOUND"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	_, err := c.GetAdUnit(context.Background(), "networks/123456/adUnits/999")
	if err == nil {
		t.Fatal("expected error for missing ad unit, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true for a 404; err = %v", err)
	}
}

func TestPatchAdUnitSendsUpdateMask(t *testing.T) {
	var gotBody map[string]any
	var gotMask string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/adUnits/456" {
			t.Errorf("path = %q, want /v1/networks/123456/adUnits/456", r.URL.Path)
		}
		gotMask = r.URL.Query().Get("updateMask")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/adUnits/456",
			"displayName": "New Name",
			"description": "New description"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	in := &AdUnit{
		Name:        "networks/123456/adUnits/456",
		DisplayName: "New Name",
		Description: "New description",
	}
	out, err := c.PatchAdUnit(context.Background(), in, []string{"displayName", "description"})
	if err != nil {
		t.Fatalf("PatchAdUnit: %v", err)
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

func TestPatchAdUnitRejectsEmptyMask(t *testing.T) {
	// An empty mask would ask the API to replace every field; the guard must
	// refuse it locally so the request never reaches the server. Mirrors
	// TestPatchPlacementRejectsEmptyMask / the custom targeting key equivalent.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	if _, err := c.PatchAdUnit(context.Background(),
		&AdUnit{Name: "networks/123456/adUnits/456"}, nil); err == nil {
		t.Fatal("expected an error for an empty update mask, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("server saw %d calls, want 0 (the empty-mask guard must not send a request)", got)
	}
}

func TestArchiveAdUnitPostsBatchBody(t *testing.T) {
	var gotBody struct {
		Names []string `json:"names"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/adUnits:batchArchive" {
			t.Errorf("path = %q, want /v1/networks/123456/adUnits:batchArchive", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	if err := c.ArchiveAdUnit(context.Background(), "networks/123456/adUnits/456"); err != nil {
		t.Fatalf("ArchiveAdUnit: %v", err)
	}
	if len(gotBody.Names) != 1 || gotBody.Names[0] != "networks/123456/adUnits/456" {
		t.Errorf("batch archive names = %v, want exactly [networks/123456/adUnits/456]", gotBody.Names)
	}
}

func TestIsNotFound(t *testing.T) {
	if IsNotFound(nil) {
		t.Error("IsNotFound(nil) = true, want false")
	}
	if IsNotFound(&APIError{StatusCode: http.StatusForbidden}) {
		t.Error("IsNotFound(403) = true, want false")
	}
	if !IsNotFound(&APIError{StatusCode: http.StatusNotFound}) {
		t.Error("IsNotFound(404) = false, want true")
	}
}

func TestNetworkCodeAccessor(t *testing.T) {
	c := testClient(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), Config{})
	if c.NetworkCode() != "123456" {
		t.Errorf("NetworkCode() = %q, want 123456", c.NetworkCode())
	}
}
