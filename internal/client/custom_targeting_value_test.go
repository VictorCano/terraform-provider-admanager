package client

// All tests run against local httptest servers with a static fake token.
// No real Google credentials are ever used or required here.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetCustomTargetingValueDecodesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/customTargetingValues/555" {
			t.Errorf("path = %q, want /v1/networks/123456/customTargetingValues/555", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/customTargetingValues/555",
			"customTargetingKey": "networks/123456/customTargetingKeys/321",
			"adTagName": "honda",
			"displayName": "Honda",
			"matchType": "EXACT",
			"status": "ACTIVE"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	out, err := c.GetCustomTargetingValue(context.Background(), "networks/123456/customTargetingValues/555")
	if err != nil {
		t.Fatalf("GetCustomTargetingValue: %v", err)
	}
	if out.Name != "networks/123456/customTargetingValues/555" ||
		out.CustomTargetingKey != "networks/123456/customTargetingKeys/321" ||
		out.AdTagName != "honda" || out.DisplayName != "Honda" ||
		out.MatchType != "EXACT" || out.Status != "ACTIVE" {
		t.Errorf("unexpected decoded value: %+v", out)
	}
}

func TestGetCustomTargetingValueNotFoundIsDistinguishable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"CustomTargetingValue not found","status":"NOT_FOUND"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	_, err := c.GetCustomTargetingValue(context.Background(), "networks/123456/customTargetingValues/999")
	if err == nil {
		t.Fatal("expected error for missing value, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true for a 404; err = %v", err)
	}
}
