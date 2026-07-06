package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestAdUnitDataSourceMetadata(t *testing.T) {
	d := NewAdUnitDataSource()
	resp := &datasource.MetadataResponse{}
	d.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "admanager"}, resp)
	if resp.TypeName != "admanager_ad_unit" {
		t.Errorf("TypeName = %q, want admanager_ad_unit", resp.TypeName)
	}
}

func TestAdUnitDataSourceSchema(t *testing.T) {
	d := NewAdUnitDataSource()
	resp := &datasource.SchemaResponse{}
	d.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	attrs := resp.Schema.Attributes

	// Same attribute set as the resource, minus the provider-side-only
	// skip_archive_on_destroy.
	wantComputed := []string{
		"id", "parent_ad_unit", "display_name", "description", "target_window",
		"effective_target_window", "explicitly_targeted", "applied_adsense_enabled",
		"effective_adsense_enabled", "smart_size_mode", "refresh_delay",
		"external_set_top_box_channel_id", "status", "has_children", "update_time",
		"sizes", "applied_teams", "teams",
	}
	for _, name := range wantComputed {
		a, ok := attrs[name]
		if !ok {
			t.Errorf("schema missing attribute %q", name)
			continue
		}
		if !a.IsComputed() {
			t.Errorf("attribute %q must be computed", name)
		}
	}
	// The two lookup keys are optional inputs and computed outputs.
	for _, name := range []string{"ad_unit_id", "ad_unit_code"} {
		a, ok := attrs[name]
		if !ok {
			t.Errorf("schema missing lookup attribute %q", name)
			continue
		}
		if !a.IsOptional() || !a.IsComputed() {
			t.Errorf("attribute %q must be optional and computed", name)
		}
	}
	// The provider-side-only resource attribute must not appear on the data source.
	if _, ok := attrs["skip_archive_on_destroy"]; ok {
		t.Error("skip_archive_on_destroy must not be exposed on the data source")
	}
}

func TestAdUnitDataSourceHasExactlyOneOfValidator(t *testing.T) {
	d, ok := NewAdUnitDataSource().(datasource.DataSourceWithConfigValidators)
	if !ok {
		t.Fatal("ad unit data source must implement DataSourceWithConfigValidators")
	}
	validators := d.ConfigValidators(context.Background())
	if len(validators) == 0 {
		t.Fatal("expected at least one config validator (ExactlyOneOf ad_unit_id/ad_unit_code)")
	}
}

func TestEscapeFilterString(t *testing.T) {
	cases := map[string]string{
		`homepage`:      `homepage`,
		`a"b`:           `a\"b`,
		`a\b`:           `a\\b`,
		`a\"b`:          `a\\\"b`,
		`plain_code-42`: `plain_code-42`,
	}
	for in, want := range cases {
		if got := escapeFilterString(in); got != want {
			t.Errorf("escapeFilterString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdUnitDataSourceResolveByFullName(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/networks/123456/adUnits/456" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"name":"networks/123456/adUnits/456","adUnitId":"456","displayName":"Homepage","parentAdUnit":"networks/123456/adUnits/1","status":"ACTIVE"}`))
	}))
	defer srv.Close()

	d := &adUnitDataSource{client: newAdUnitTestClient(t, srv)}
	au, diags := d.resolveAdUnit(ctx, types.StringValue("networks/123456/adUnits/456"), types.StringNull())
	if diags.HasError() {
		t.Fatalf("resolveAdUnit: %v", diags)
	}
	if au == nil || au.AdUnitID != "456" {
		t.Errorf("resolved ad unit = %+v, want id 456", au)
	}
}

func TestAdUnitDataSourceResolveByBareID(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A bare numeric id must be expanded to the full resource-name path.
		if r.URL.Path != "/v1/networks/123456/adUnits/456" {
			t.Errorf("path = %q, want the bare id expanded to a full resource name", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"name":"networks/123456/adUnits/456","adUnitId":"456","displayName":"Homepage"}`))
	}))
	defer srv.Close()

	d := &adUnitDataSource{client: newAdUnitTestClient(t, srv)}
	au, diags := d.resolveAdUnit(ctx, types.StringValue("456"), types.StringNull())
	if diags.HasError() {
		t.Fatalf("resolveAdUnit: %v", diags)
	}
	if au == nil || au.Name != "networks/123456/adUnits/456" {
		t.Errorf("resolved ad unit = %+v", au)
	}
}

func TestAdUnitDataSourceResolveInvalidID(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a malformed id must not reach the API; got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	d := &adUnitDataSource{client: newAdUnitTestClient(t, srv)}
	_, diags := d.resolveAdUnit(ctx, types.StringValue("adUnits/456"), types.StringNull())
	if !diags.HasError() {
		t.Fatal("expected an error for a malformed ad_unit_id, got none")
	}
}

func TestAdUnitDataSourceResolveNotFound(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"AdUnit not found"}}`))
	}))
	defer srv.Close()

	d := &adUnitDataSource{client: newAdUnitTestClient(t, srv)}
	_, diags := d.resolveAdUnit(ctx, types.StringValue("999"), types.StringNull())
	if !diags.HasError() {
		t.Fatal("expected a not-found error, got none")
	}
}

func TestAdUnitDataSourceResolveByCode(t *testing.T) {
	ctx := context.Background()
	var gotFilter, gotPageSize string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/networks/123456/adUnits" {
			t.Errorf("path = %q, want the list collection path", r.URL.Path)
		}
		gotFilter = r.URL.Query().Get("filter")
		gotPageSize = r.URL.Query().Get("pageSize")
		_, _ = w.Write([]byte(`{"adUnits":[{"name":"networks/123456/adUnits/456","adUnitId":"456","adUnitCode":"homepage","displayName":"Homepage"}]}`))
	}))
	defer srv.Close()

	d := &adUnitDataSource{client: newAdUnitTestClient(t, srv)}
	au, diags := d.resolveAdUnit(ctx, types.StringNull(), types.StringValue("homepage"))
	if diags.HasError() {
		t.Fatalf("resolveAdUnit: %v", diags)
	}
	if au == nil || au.AdUnitCode != "homepage" {
		t.Errorf("resolved ad unit = %+v", au)
	}
	if gotFilter != `adUnitCode = "homepage"` {
		t.Errorf("filter = %q, want adUnitCode = \"homepage\"", gotFilter)
	}
	// The by-code lookup must request the max page size so a unique-code match
	// stays a single rate-limited round trip. A regression dropping the
	// PageSize argument would fall back to the API default and go uncaught here.
	if gotPageSize != "1000" {
		t.Errorf("pageSize = %q, want 1000 (adUnitListPageSize)", gotPageSize)
	}
}

func TestAdUnitDataSourceResolveByCodeNoMatch(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"adUnits":[]}`))
	}))
	defer srv.Close()

	d := &adUnitDataSource{client: newAdUnitTestClient(t, srv)}
	_, diags := d.resolveAdUnit(ctx, types.StringNull(), types.StringValue("missing"))
	if !diags.HasError() {
		t.Fatal("expected a not-found error for an unmatched code, got none")
	}
}

func TestAdUnitDataSourceResolveByCodeMultipleMatches(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"adUnits":[
			{"name":"networks/123456/adUnits/1","adUnitCode":"dup"},
			{"name":"networks/123456/adUnits/2","adUnitCode":"dup"}
		]}`))
	}))
	defer srv.Close()

	d := &adUnitDataSource{client: newAdUnitTestClient(t, srv)}
	_, diags := d.resolveAdUnit(ctx, types.StringNull(), types.StringValue("dup"))
	if !diags.HasError() {
		t.Fatal("expected an error when a code matches more than one ad unit, got none")
	}
	if !strings.Contains(diags.Errors()[0].Detail(), "2") {
		t.Errorf("multi-match error should report the count; got %q", diags.Errors()[0].Detail())
	}
}

func TestAdUnitDataSourceModelFromAPIReusesMapping(t *testing.T) {
	ctx := context.Background()
	m, diags := adUnitDataSourceModelFromAPI(ctx, apiAdUnit())
	if diags.HasError() {
		t.Fatalf("adUnitDataSourceModelFromAPI: %v", diags)
	}
	// Spot-check that the shared mapping populated the embedded model, including
	// nested sizes and the applied/effective teams split.
	if m.ID.ValueString() != "networks/123456/adUnits/456" || m.DisplayName.ValueString() != "Homepage Leaderboard" {
		t.Errorf("basic fields not mapped: %+v", m)
	}
	if len(m.Sizes) != 2 || len(m.Sizes[1].Companions) != 1 {
		t.Errorf("nested sizes not mapped: %+v", m.Sizes)
	}
	var applied, all []string
	m.AppliedTeams.ElementsAs(ctx, &applied, false)
	m.Teams.ElementsAs(ctx, &all, false)
	if len(applied) != 1 || len(all) != 2 {
		t.Errorf("teams mapping wrong: applied=%v teams=%v", applied, all)
	}
}
