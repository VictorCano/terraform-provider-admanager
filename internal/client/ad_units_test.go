package client

// All tests run against local httptest servers with a static fake token.
// No real Google credentials are ever used or required here.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListAdUnitsPaginatesAllPages(t *testing.T) {
	var gotFilters, gotTokens []string
	var gotPageSizes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/networks/123456/adUnits" {
			t.Errorf("path = %q, want /v1/networks/123456/adUnits", r.URL.Path)
		}
		gotFilters = append(gotFilters, r.URL.Query().Get("filter"))
		gotPageSizes = append(gotPageSizes, r.URL.Query().Get("pageSize"))
		token := r.URL.Query().Get("pageToken")
		gotTokens = append(gotTokens, token)
		switch token {
		case "":
			_, _ = w.Write([]byte(`{
				"adUnits": [{"name": "networks/123456/adUnits/1", "adUnitId": "1", "displayName": "One"}],
				"nextPageToken": "page-2"
			}`))
		case "page-2":
			_, _ = w.Write([]byte(`{
				"adUnits": [{"name": "networks/123456/adUnits/2", "adUnitId": "2", "displayName": "Two"}]
			}`))
		default:
			t.Errorf("unexpected pageToken %q", token)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	units, err := c.ListAdUnits(context.Background(), ListAdUnitsOptions{Filter: `adUnitCode = "x"`, PageSize: 1000})
	if err != nil {
		t.Fatalf("ListAdUnits: %v", err)
	}
	if len(units) != 2 || units[0].AdUnitID != "1" || units[1].AdUnitID != "2" {
		t.Fatalf("units = %+v, want two units 1 and 2 accumulated across pages", units)
	}
	// The filter must ride along on every page, not just the first.
	if len(gotFilters) != 2 || gotFilters[0] != `adUnitCode = "x"` || gotFilters[1] != `adUnitCode = "x"` {
		t.Errorf("filters per page = %v, want the same filter on both pages", gotFilters)
	}
	// pageSize must ride along on every page too.
	if len(gotPageSizes) != 2 || gotPageSizes[0] != "1000" || gotPageSizes[1] != "1000" {
		t.Errorf("pageSizes per page = %v, want 1000 on both pages", gotPageSizes)
	}
	// The second request must carry the token from the first response.
	if len(gotTokens) != 2 || gotTokens[0] != "" || gotTokens[1] != "page-2" {
		t.Errorf("pageTokens = %v, want [\"\", \"page-2\"]", gotTokens)
	}
}

func TestListAdUnitsOmitsEmptyOptionalParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// With no options set, no optional query params must be sent.
		for _, p := range []string{"filter", "orderBy", "pageSize", "pageToken"} {
			if _, ok := q[p]; ok {
				t.Errorf("query param %q should be absent when unset, got %q", p, q.Get(p))
			}
		}
		_, _ = w.Write([]byte(`{"adUnits": []}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	if _, err := c.ListAdUnits(context.Background(), ListAdUnitsOptions{}); err != nil {
		t.Fatalf("ListAdUnits: %v", err)
	}
}

func TestListAdUnitsSafetyCapErrors(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Always dangle a next page so pagination never terminates naturally.
		_, _ = w.Write([]byte(`{
			"adUnits": [{"name": "networks/123456/adUnits/1"}],
			"nextPageToken": "always-more"
		}`))
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	c.maxListPages = 2
	_, err := c.ListAdUnits(context.Background(), ListAdUnitsOptions{Filter: `status = "ACTIVE"`})
	if err == nil {
		t.Fatal("expected a safety-cap error when pagination never terminates, got nil")
	}
	// No silent truncation: the error must explain the filter matched too many.
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("cap error = %q, want it to mention the filter matched too many ad units", err.Error())
	}
	// It must stop at the cap, not loop forever.
	if calls != 2 {
		t.Errorf("server saw %d calls, want exactly 2 (the cap)", calls)
	}
}
