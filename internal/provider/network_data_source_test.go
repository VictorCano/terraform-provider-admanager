package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

func TestNetworkDataSourceMetadata(t *testing.T) {
	d := NewNetworkDataSource()
	resp := &datasource.MetadataResponse{}
	d.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "admanager"}, resp)
	if resp.TypeName != "admanager_network" {
		t.Errorf("TypeName = %q, want admanager_network", resp.TypeName)
	}
}

func TestNetworkDataSourceSchema(t *testing.T) {
	d := NewNetworkDataSource()
	resp := &datasource.SchemaResponse{}
	d.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	attrs := resp.Schema.Attributes
	want := []string{
		"id", "network_code", "display_name", "time_zone", "currency_code",
		"secondary_currency_codes", "effective_root_ad_unit", "network_id",
		"property_code", "test_network",
	}
	for _, name := range want {
		a, ok := attrs[name]
		if !ok {
			t.Errorf("schema missing attribute %q", name)
			continue
		}
		// A no-argument data source: every attribute is computed, none settable.
		if !a.IsComputed() {
			t.Errorf("attribute %q must be computed", name)
		}
		if a.IsRequired() || a.IsOptional() {
			t.Errorf("attribute %q must not be required or optional", name)
		}
	}
}

// networkDataSourceSchema returns the data source schema for building configs
// and states in unit tests.
func networkDataSourceSchema(t *testing.T) dschema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	NewNetworkDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestNetworkDataSourceRead(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/networks/123456" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456",
			"networkCode": "123456",
			"displayName": "Test Network",
			"timeZone": "America/Sao_Paulo",
			"currencyCode": "BRL",
			"secondaryCurrencyCodes": ["USD", "EUR"],
			"effectiveRootAdUnit": "networks/123456/adUnits/1",
			"networkId": "9999",
			"propertyCode": "prop-1",
			"testNetwork": true
		}`))
	}))
	defer srv.Close()

	d := &networkDataSource{client: newAdUnitTestClient(t, srv)}
	sch := networkDataSourceSchema(t)
	// A no-argument data source reads nothing from config, so an empty request is
	// faithful; only the response State needs the schema attached.
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	d.Read(ctx, datasource.ReadRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", resp.Diagnostics)
	}

	var got networkDataSourceModel
	if diag := resp.State.Get(ctx, &got); diag.HasError() {
		t.Fatalf("reading state: %v", diag)
	}
	if got.ID.ValueString() != "networks/123456" {
		t.Errorf("id = %q, want networks/123456", got.ID.ValueString())
	}
	if got.NetworkCode.ValueString() != "123456" || got.DisplayName.ValueString() != "Test Network" ||
		got.TimeZone.ValueString() != "America/Sao_Paulo" || got.CurrencyCode.ValueString() != "BRL" ||
		got.EffectiveRootAdUnit.ValueString() != "networks/123456/adUnits/1" ||
		got.NetworkID.ValueString() != "9999" || got.PropertyCode.ValueString() != "prop-1" {
		t.Errorf("unexpected mapped network: %+v", got)
	}
	if !got.TestNetwork.ValueBool() {
		t.Error("test_network should be true")
	}
	var secondary []string
	got.SecondaryCurrencyCodes.ElementsAs(ctx, &secondary, false)
	if len(secondary) != 2 || secondary[0] != "USD" || secondary[1] != "EUR" {
		t.Errorf("secondary_currency_codes = %v, want [USD EUR]", secondary)
	}
}

func TestNetworkDataSourceReadEmptySecondaryCurrencies(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"name": "networks/123456",
			"networkCode": "123456",
			"displayName": "Test Network",
			"testNetwork": false
		}`))
	}))
	defer srv.Close()

	d := &networkDataSource{client: newAdUnitTestClient(t, srv)}
	sch := networkDataSourceSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	d.Read(ctx, datasource.ReadRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", resp.Diagnostics)
	}

	var got networkDataSourceModel
	_ = resp.State.Get(ctx, &got)
	// A computed list attribute must be a known empty list, never unknown/null,
	// so the value is honest and Terraform accepts the read result.
	if got.SecondaryCurrencyCodes.IsNull() || got.SecondaryCurrencyCodes.IsUnknown() {
		t.Errorf("secondary_currency_codes = %v, want known empty list", got.SecondaryCurrencyCodes)
	}
	var secondary []string
	got.SecondaryCurrencyCodes.ElementsAs(ctx, &secondary, false)
	if len(secondary) != 0 {
		t.Errorf("secondary_currency_codes = %v, want empty", secondary)
	}
	// Absent optional strings map to null, not empty string.
	if !got.PropertyCode.IsNull() {
		t.Errorf("property_code = %v, want null when absent", got.PropertyCode)
	}
	if !got.TimeZone.IsNull() {
		t.Errorf("time_zone = %v, want null when absent", got.TimeZone)
	}
	// A well-known type placeholder must remain: types.Bool zero value false.
	if got.TestNetwork.ValueBool() {
		t.Error("test_network should be false")
	}
}
