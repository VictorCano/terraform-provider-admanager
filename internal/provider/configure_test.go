package provider

// P0-5: Configure error paths for the provider, all four resources, and all
// three data sources — none of which had a test before. Covers:
//   - each resource/data source Configure with nil ProviderData (must no-op)
//     and with a wrong ProviderData type (must add the exact "Unexpected …
//     configure type" diagnostic);
//   - customTargetingValueResource.Configure wiring the SOAP shim from the REST
//     client's shared limiter (the "nothing builds a second token bucket"
//     invariant);
//   - admanagerProvider.Configure unknown-value and client.New-failure branches,
//     including the credential-safe guarantee (a bad credentials value never
//     appears in any diagnostic).

import (
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// --- resources ---------------------------------------------------------------

func TestResourceConfigureNilProviderDataIsNoop(t *testing.T) {
	ctx := context.Background()
	factories := map[string]func() resource.Resource{
		"ad_unit":                NewAdUnitResource,
		"placement":              NewPlacementResource,
		"custom_targeting_key":   NewCustomTargetingKeyResource,
		"custom_targeting_value": NewCustomTargetingValueResource,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			r, ok := factory().(resource.ResourceWithConfigure)
			if !ok {
				t.Fatalf("%s must implement resource.ResourceWithConfigure", name)
			}
			resp := &resource.ConfigureResponse{}
			// ProviderData is nil before the provider is configured (e.g. during
			// schema validation); Configure must return cleanly.
			r.Configure(ctx, resource.ConfigureRequest{ProviderData: nil}, resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("nil ProviderData must be a clean no-op; diags = %v", resp.Diagnostics)
			}
		})
	}
}

func TestResourceConfigureWrongTypeErrors(t *testing.T) {
	ctx := context.Background()
	factories := map[string]func() resource.Resource{
		"ad_unit":                NewAdUnitResource,
		"placement":              NewPlacementResource,
		"custom_targeting_key":   NewCustomTargetingKeyResource,
		"custom_targeting_value": NewCustomTargetingValueResource,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			r := factory().(resource.ResourceWithConfigure)
			resp := &resource.ConfigureResponse{}
			r.Configure(ctx, resource.ConfigureRequest{ProviderData: "not-a-client"}, resp)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("a wrong ProviderData type must produce an error diagnostic")
			}
			d := resp.Diagnostics.Errors()[0]
			if d.Summary() != "Unexpected resource configure type" {
				t.Errorf("summary = %q, want %q", d.Summary(), "Unexpected resource configure type")
			}
			if !strings.Contains(d.Detail(), "Expected *client.Client, got string") {
				t.Errorf("detail should name the expected and actual types: %q", d.Detail())
			}
		})
	}
}

// TestCustomTargetingValueConfigureWiresSOAPFromSharedLimiter verifies that a
// successful Configure with a real *client.Client stores the REST client and
// builds the SOAP shim from that client's shared token bucket — the invariant
// that nothing spins up a second limiter (custom_targeting_value_resource.go
// wires soap.NewClient from c.Limiter()). The SOAP client exposes no limiter
// accessor, so the shared-pointer check reads the unexported field via reflect.
func TestCustomTargetingValueConfigureWiresSOAPFromSharedLimiter(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(nil) // Configure makes no HTTP calls.
	defer srv.Close()
	c := newAdUnitTestClient(t, srv)

	r := &customTargetingValueResource{}
	resp := &resource.ConfigureResponse{}
	r.Configure(ctx, resource.ConfigureRequest{ProviderData: c}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Configure with a real client must not error: %v", resp.Diagnostics)
	}
	if r.client != c {
		t.Error("Configure must store the REST client")
	}
	if r.soap == nil {
		t.Fatal("Configure must build the SOAP shim")
	}
	soapLimiter := reflect.ValueOf(r.soap).Elem().FieldByName("limiter").Pointer()
	if soapLimiter != reflect.ValueOf(c.Limiter()).Pointer() {
		t.Error("SOAP client must share the REST client's limiter, not build a second token bucket")
	}
}

// --- data sources ------------------------------------------------------------

func TestDataSourceConfigureNilProviderDataIsNoop(t *testing.T) {
	ctx := context.Background()
	factories := map[string]func() datasource.DataSource{
		"network":  NewNetworkDataSource,
		"ad_unit":  NewAdUnitDataSource,
		"ad_units": NewAdUnitsDataSource,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			d := factory().(datasource.DataSourceWithConfigure)
			resp := &datasource.ConfigureResponse{}
			d.Configure(ctx, datasource.ConfigureRequest{ProviderData: nil}, resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("nil ProviderData must be a clean no-op; diags = %v", resp.Diagnostics)
			}
		})
	}
}

func TestDataSourceConfigureWrongTypeErrors(t *testing.T) {
	ctx := context.Background()
	factories := map[string]func() datasource.DataSource{
		"network":  NewNetworkDataSource,
		"ad_unit":  NewAdUnitDataSource,
		"ad_units": NewAdUnitsDataSource,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			d := factory().(datasource.DataSourceWithConfigure)
			resp := &datasource.ConfigureResponse{}
			d.Configure(ctx, datasource.ConfigureRequest{ProviderData: 42}, resp)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("a wrong ProviderData type must produce an error diagnostic")
			}
			dg := resp.Diagnostics.Errors()[0]
			if dg.Summary() != "Unexpected data source configure type" {
				t.Errorf("summary = %q, want %q", dg.Summary(), "Unexpected data source configure type")
			}
			if !strings.Contains(dg.Detail(), "Expected *client.Client, got int") {
				t.Errorf("detail should name the expected and actual types: %q", dg.Detail())
			}
		})
	}
}

// --- provider ----------------------------------------------------------------

// newProviderConfig builds a tfsdk.Config for the provider schema from model.
func newProviderConfig(t *testing.T, p provider.Provider, model providerModel) tfsdk.Config {
	t.Helper()
	schemaResp := &provider.SchemaResponse{}
	p.Schema(context.Background(), provider.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("provider schema: %v", schemaResp.Diagnostics)
	}
	// tfsdk.Config is read-only (no Set); marshal the model through a State,
	// which shares the same Schema/Raw shape, then reuse its Raw.
	st := tfsdk.State{Schema: schemaResp.Schema}
	if d := st.Set(context.Background(), &model); d.HasError() {
		t.Fatalf("building provider config: %v", d)
	}
	return tfsdk.Config{Schema: schemaResp.Schema, Raw: st.Raw}
}

func TestProviderConfigureUnknownNetworkCodeErrors(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	cfg := newProviderConfig(t, p, providerModel{
		NetworkCode:       types.StringUnknown(), // known only after apply -> cannot configure a client
		Credentials:       types.StringNull(),
		RequestsPerSecond: types.Float64Null(),
		RetryMaxAttempts:  types.Int64Null(),
	})
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, provider.ConfigureRequest{Config: cfg}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unknown network_code must produce an error diagnostic")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unknown network code" {
		t.Errorf("summary = %q, want %q", got, "Unknown network code")
	}
	// A client must NOT be handed downstream when configuration failed.
	if resp.ResourceData != nil || resp.DataSourceData != nil {
		t.Error("no client should be published when Configure errors")
	}
}

// TestProviderConfigureBadCredentialsIsCredentialSafe drives the client.New
// failure branch with a credentials value that resolveCredentialsJSON rejects,
// and asserts the historical HIGH-severity leak class stays closed: the bad
// credentials string must appear in NO diagnostic text (summary or detail).
func TestProviderConfigureBadCredentialsIsCredentialSafe(t *testing.T) {
	ctx := context.Background()
	const secret = `SECRET_MARKER_"leak_me`
	p := New("test")()
	cfg := newProviderConfig(t, p, providerModel{
		NetworkCode:       types.StringValue("123456"),
		Credentials:       types.StringValue(secret),
		RequestsPerSecond: types.Float64Null(),
		RetryMaxAttempts:  types.Int64Null(),
	})
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, provider.ConfigureRequest{Config: cfg}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a malformed credentials value must fail client.New")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unable to create Ad Manager API client" {
		t.Errorf("summary = %q, want %q", got, "Unable to create Ad Manager API client")
	}
	for _, d := range resp.Diagnostics {
		if strings.Contains(d.Summary(), "SECRET_MARKER") || strings.Contains(d.Detail(), "SECRET_MARKER") {
			t.Errorf("credentials value leaked into a diagnostic: %q / %q", d.Summary(), d.Detail())
		}
	}
}
