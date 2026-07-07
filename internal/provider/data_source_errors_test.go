package provider

// P0-7: data-source Read error / not-found paths, driven credential-free against
// a fake server. Each resource is exercised with a 404 and a 5xx and must emit a
// clean diagnostic (no panic) whose summary names the failing resource; the
// plural ad_units data source additionally handles an empty result as an empty
// list rather than an error.
//
// 5xx on a GET is retried by the client, so those cases use a one-attempt client
// to avoid burning the exponential backoff in the test.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/oauth2"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// newOneAttemptTestClient builds a client that never retries, so a 5xx fixture
// returns immediately instead of spinning through the backoff schedule.
func newOneAttemptTestClient(t *testing.T, srv *httptest.Server) *client.Client {
	t.Helper()
	c, err := client.New(context.Background(), client.Config{
		NetworkCode:      "123456",
		BaseURL:          srv.URL,
		TokenSource:      oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}),
		RetryMaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c
}

func serverError(message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"status":"INTERNAL","message":"` + message + `"}}`))
	}
}

// --- network -----------------------------------------------------------------

func TestNetworkDataSourceReadNotFound(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(notFoundHandler("Network not found"))
	defer srv.Close()

	d := &networkDataSource{client: newAdUnitTestClient(t, srv)}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: networkDataSourceSchema(t)}}
	d.Read(ctx, datasource.ReadRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a 404 must surface an error diagnostic")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unable to read network" {
		t.Errorf("summary = %q, want %q", got, "Unable to read network")
	}
}

func TestNetworkDataSourceReadServerError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(serverError("boom"))
	defer srv.Close()

	d := &networkDataSource{client: newOneAttemptTestClient(t, srv)}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: networkDataSourceSchema(t)}}
	d.Read(ctx, datasource.ReadRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a 500 must surface an error diagnostic")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unable to read network" {
		t.Errorf("summary = %q, want %q", got, "Unable to read network")
	}
}

// --- ad_unit (singular) ------------------------------------------------------

func TestAdUnitDataSourceResolveByIDServerError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(serverError("boom"))
	defer srv.Close()

	d := &adUnitDataSource{client: newOneAttemptTestClient(t, srv)}
	_, diags := d.resolveAdUnit(ctx, types.StringValue("456"), types.StringNull())
	if !diags.HasError() {
		t.Fatal("a 500 on the by-id lookup must surface an error diagnostic")
	}
	if got := diags.Errors()[0].Summary(); got != "Unable to read ad unit" {
		t.Errorf("summary = %q, want %q", got, "Unable to read ad unit")
	}
}

func TestAdUnitDataSourceResolveByCodeServerError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(serverError("boom"))
	defer srv.Close()

	d := &adUnitDataSource{client: newOneAttemptTestClient(t, srv)}
	_, diags := d.resolveAdUnit(ctx, types.StringNull(), types.StringValue("homepage"))
	if !diags.HasError() {
		t.Fatal("a 500 on the by-code list lookup must surface an error diagnostic")
	}
	if got := diags.Errors()[0].Summary(); got != "Unable to look up ad unit by code" {
		t.Errorf("summary = %q, want %q", got, "Unable to look up ad unit by code")
	}
}

// --- ad_units (plural) -------------------------------------------------------

func TestAdUnitsDataSourceReadServerError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(serverError("boom"))
	defer srv.Close()

	d := &adUnitsDataSource{client: newOneAttemptTestClient(t, srv)}
	summaries, diags := d.listSummaries(ctx, types.StringNull())
	if !diags.HasError() {
		t.Fatal("a 500 on the list must surface an error diagnostic")
	}
	if got := diags.Errors()[0].Summary(); got != "Unable to list ad units" {
		t.Errorf("summary = %q, want %q", got, "Unable to list ad units")
	}
	if summaries != nil {
		t.Errorf("summaries = %v, want nil on error", summaries)
	}
}

// TestAdUnitsDataSourceReadEmptyIsEmptyList drives Read end-to-end with an empty
// API result and asserts the state holds an empty (not null, not errored)
// ad_units list — a zero-match list is a valid, iterable result, not a failure.
func TestAdUnitsDataSourceReadEmptyIsEmptyList(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"adUnits":[]}`))
	}))
	defer srv.Close()

	ds := &adUnitsDataSource{client: newAdUnitTestClient(t, srv)}
	schemaResp := &datasource.SchemaResponse{}
	NewAdUnitsDataSource().Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	sch := schemaResp.Schema
	// tfsdk.Config is read-only; marshal a null-filter config through a State,
	// which shares the Schema/Raw shape, then reuse its Raw.
	st := tfsdk.State{Schema: sch}
	if d := st.Set(ctx, &adUnitsDataSourceModel{Filter: types.StringNull()}); d.HasError() {
		t.Fatalf("building config: %v", d)
	}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sch, Raw: st.Raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("an empty result must not error: %v", resp.Diagnostics)
	}
	var got adUnitsDataSourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.AdUnits == nil {
		t.Error("ad_units must be a non-nil empty list, not null")
	}
	if len(got.AdUnits) != 0 {
		t.Errorf("ad_units = %v, want empty", got.AdUnits)
	}
}
