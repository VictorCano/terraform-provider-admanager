package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

func TestAdUnitsDataSourceMetadata(t *testing.T) {
	d := NewAdUnitsDataSource()
	resp := &datasource.MetadataResponse{}
	d.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "admanager"}, resp)
	if resp.TypeName != "admanager_ad_units" {
		t.Errorf("TypeName = %q, want admanager_ad_units", resp.TypeName)
	}
}

func TestAdUnitsDataSourceSchema(t *testing.T) {
	d := NewAdUnitsDataSource()
	resp := &datasource.SchemaResponse{}
	d.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	attrs := resp.Schema.Attributes

	filter, ok := attrs["filter"]
	if !ok {
		t.Fatal("schema missing filter attribute")
	}
	if !filter.IsOptional() {
		t.Error("filter must be optional")
	}
	if filter.IsRequired() {
		t.Error("filter must not be required")
	}

	adUnits, ok := attrs["ad_units"]
	if !ok {
		t.Fatal("schema missing ad_units attribute")
	}
	if !adUnits.IsComputed() {
		t.Error("ad_units must be computed")
	}
}

func TestAdUnitSummariesFromAPI(t *testing.T) {
	units := []client.AdUnit{
		{
			Name:         "networks/123456/adUnits/1",
			AdUnitID:     "1",
			ParentAdUnit: "networks/123456/adUnits/0",
			DisplayName:  "Child",
			AdUnitCode:   "child_code",
			Status:       "ACTIVE",
		},
		{
			// Root ad unit: no parent, no code assigned yet.
			Name:        "networks/123456/adUnits/0",
			AdUnitID:    "0",
			DisplayName: "Root",
			Status:      "ACTIVE",
		},
	}
	got := adUnitSummariesFromAPI(units)
	if len(got) != 2 {
		t.Fatalf("summaries len = %d, want 2", len(got))
	}
	if got[0].ID.ValueString() != "networks/123456/adUnits/1" || got[0].AdUnitID.ValueString() != "1" ||
		got[0].ParentAdUnit.ValueString() != "networks/123456/adUnits/0" ||
		got[0].DisplayName.ValueString() != "Child" || got[0].AdUnitCode.ValueString() != "child_code" ||
		got[0].Status.ValueString() != "ACTIVE" {
		t.Errorf("child summary wrong: %+v", got[0])
	}
	// Absent parent/code on the root must be null, not empty string.
	if !got[1].ParentAdUnit.IsNull() {
		t.Errorf("root parent_ad_unit = %v, want null", got[1].ParentAdUnit)
	}
	if !got[1].AdUnitCode.IsNull() {
		t.Errorf("root ad_unit_code = %v, want null", got[1].AdUnitCode)
	}
}

func TestAdUnitSummariesFromAPIEmptyIsNonNil(t *testing.T) {
	// A zero-match result must serialize as an empty list `[]`, not null, so the
	// ad_units attribute stays iterable in a `for` expression.
	if got := adUnitSummariesFromAPI(nil); got == nil {
		t.Error("adUnitSummariesFromAPI(nil) = nil, want a non-nil empty slice")
	}
	if got := adUnitSummariesFromAPI([]client.AdUnit{}); got == nil || len(got) != 0 {
		t.Errorf("adUnitSummariesFromAPI(empty) = %v, want non-nil empty slice", got)
	}
}

func TestAdUnitsDataSourceListSummariesPassesFilterAndPaginates(t *testing.T) {
	ctx := context.Background()
	var gotFilters, gotPageSizes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/networks/123456/adUnits" {
			t.Errorf("path = %q, want the list collection path", r.URL.Path)
		}
		gotFilters = append(gotFilters, r.URL.Query().Get("filter"))
		gotPageSizes = append(gotPageSizes, r.URL.Query().Get("pageSize"))
		switch r.URL.Query().Get("pageToken") {
		case "":
			_, _ = w.Write([]byte(`{"adUnits":[{"name":"networks/123456/adUnits/1","adUnitId":"1","displayName":"One","status":"ACTIVE"}],"nextPageToken":"p2"}`))
		case "p2":
			_, _ = w.Write([]byte(`{"adUnits":[{"name":"networks/123456/adUnits/2","adUnitId":"2","displayName":"Two","status":"ACTIVE"}]}`))
		default:
			t.Errorf("unexpected pageToken")
		}
	}))
	defer srv.Close()

	d := &adUnitsDataSource{client: newAdUnitTestClient(t, srv)}
	summaries, diags := d.listSummaries(ctx, types.StringValue(`parentAdUnit = "networks/123456/adUnits/0"`))
	if diags.HasError() {
		t.Fatalf("listSummaries: %v", diags)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %d, want 2 accumulated across pages", len(summaries))
	}
	for i, f := range gotFilters {
		if f != `parentAdUnit = "networks/123456/adUnits/0"` {
			t.Errorf("page %d filter = %q, want the passthrough filter", i, f)
		}
	}
	// The max page size must ride on every page so large result sets cost as
	// few rate-limited round trips as the API allows. A regression dropping the
	// PageSize argument would fall back to the API default and go uncaught here.
	for i, ps := range gotPageSizes {
		if ps != "1000" {
			t.Errorf("page %d pageSize = %q, want 1000 (adUnitListPageSize)", i, ps)
		}
	}
}

func TestAdUnitsDataSourceListSummariesNoFilter(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["filter"]; ok {
			t.Errorf("no filter param should be sent when filter is null; got %q", r.URL.Query().Get("filter"))
		}
		_, _ = w.Write([]byte(`{"adUnits":[]}`))
	}))
	defer srv.Close()

	d := &adUnitsDataSource{client: newAdUnitTestClient(t, srv)}
	summaries, diags := d.listSummaries(ctx, types.StringNull())
	if diags.HasError() {
		t.Fatalf("listSummaries: %v", diags)
	}
	if len(summaries) != 0 {
		t.Errorf("summaries = %v, want empty", summaries)
	}
}
