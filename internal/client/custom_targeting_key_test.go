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

func TestCreateCustomTargetingKeyPostsCorrectPathAndBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/customTargetingKeys" {
			t.Errorf("path = %q, want /v1/networks/123456/customTargetingKeys", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/customTargetingKeys/321",
			"customTargetingKeyId": "321",
			"adTagName": "genre",
			"displayName": "Genre",
			"type": "FREEFORM",
			"reportableType": "ON",
			"status": "ACTIVE"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	in := &CustomTargetingKey{
		AdTagName:      "genre",
		DisplayName:    "Genre",
		Type:           "FREEFORM",
		ReportableType: "ON",
	}
	out, err := c.CreateCustomTargetingKey(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateCustomTargetingKey: %v", err)
	}
	if gotBody["adTagName"] != "genre" || gotBody["type"] != "FREEFORM" || gotBody["reportableType"] != "ON" {
		t.Errorf("request body missing settable fields: %v", gotBody)
	}
	// Output-only fields must not be sent on create.
	for _, k := range []string{"status", "customTargetingKeyId"} {
		if _, ok := gotBody[k]; ok {
			t.Errorf("request body must not carry output-only %s: %v", k, gotBody)
		}
	}
	if out.Name != "networks/123456/customTargetingKeys/321" || out.CustomTargetingKeyID != "321" ||
		out.Type != "FREEFORM" || out.ReportableType != "ON" || out.Status != "ACTIVE" {
		t.Errorf("unexpected decoded key: %+v", out)
	}
}

func TestGetCustomTargetingKeyDecodesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/customTargetingKeys/321" {
			t.Errorf("path = %q, want /v1/networks/123456/customTargetingKeys/321", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/customTargetingKeys/321",
			"customTargetingKeyId": "321",
			"adTagName": "genre",
			"displayName": "Genre",
			"type": "FREEFORM",
			"reportableType": "ON",
			"status": "ACTIVE"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	out, err := c.GetCustomTargetingKey(context.Background(), "networks/123456/customTargetingKeys/321")
	if err != nil {
		t.Fatalf("GetCustomTargetingKey: %v", err)
	}
	if out.AdTagName != "genre" || out.Type != "FREEFORM" || out.Status != "ACTIVE" {
		t.Errorf("unexpected decoded key: %+v", out)
	}
}

func TestGetCustomTargetingKeyNotFoundIsDistinguishable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"CustomTargetingKey not found","status":"NOT_FOUND"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	_, err := c.GetCustomTargetingKey(context.Background(), "networks/123456/customTargetingKeys/999")
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true for a 404; err = %v", err)
	}
}

func TestPatchCustomTargetingKeySendsUpdateMask(t *testing.T) {
	var gotBody map[string]any
	var gotMask string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/customTargetingKeys/321" {
			t.Errorf("path = %q, want /v1/networks/123456/customTargetingKeys/321", r.URL.Path)
		}
		gotMask = r.URL.Query().Get("updateMask")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/customTargetingKeys/321",
			"displayName": "New Genre"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	in := &CustomTargetingKey{
		Name:        "networks/123456/customTargetingKeys/321",
		DisplayName: "New Genre",
	}
	out, err := c.PatchCustomTargetingKey(context.Background(), in, []string{"displayName"})
	if err != nil {
		t.Fatalf("PatchCustomTargetingKey: %v", err)
	}
	if gotMask != "displayName" {
		t.Errorf("updateMask = %q, want displayName", gotMask)
	}
	if gotBody["displayName"] != "New Genre" {
		t.Errorf("unexpected patch body: %v", gotBody)
	}
	if out.DisplayName != "New Genre" {
		t.Errorf("decoded displayName = %q", out.DisplayName)
	}
}

func TestPatchCustomTargetingKeyRejectsEmptyMask(t *testing.T) {
	c := testClient(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), Config{})
	if _, err := c.PatchCustomTargetingKey(context.Background(), &CustomTargetingKey{Name: "networks/123456/customTargetingKeys/321"}, nil); err == nil {
		t.Fatal("expected an error for an empty update mask, got nil")
	}
}

func TestDeactivateCustomTargetingKeyPostsBatchBody(t *testing.T) {
	var gotBody struct {
		Names []string `json:"names"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/customTargetingKeys:batchDeactivate" {
			t.Errorf("path = %q, want /v1/networks/123456/customTargetingKeys:batchDeactivate", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	if err := c.DeactivateCustomTargetingKey(context.Background(), "networks/123456/customTargetingKeys/321"); err != nil {
		t.Fatalf("DeactivateCustomTargetingKey: %v", err)
	}
	if len(gotBody.Names) != 1 || gotBody.Names[0] != "networks/123456/customTargetingKeys/321" {
		t.Errorf("batch deactivate names = %v, want exactly [networks/123456/customTargetingKeys/321]", gotBody.Names)
	}
}
