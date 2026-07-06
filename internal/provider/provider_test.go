package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProviderMetadata(t *testing.T) {
	p := New("1.2.3")()
	resp := &provider.MetadataResponse{}
	p.Metadata(context.Background(), provider.MetadataRequest{}, resp)
	if resp.TypeName != "admanager" {
		t.Errorf("TypeName = %q, want admanager", resp.TypeName)
	}
	if resp.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", resp.Version)
	}
}

func TestProviderSchema(t *testing.T) {
	p := New("test")()
	resp := &provider.SchemaResponse{}
	p.Schema(context.Background(), provider.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	for _, name := range []string{"network_code", "credentials", "requests_per_second", "retry_max_attempts"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("schema missing attribute %q", name)
		}
	}
	// Service account JSON must never end up in plan output or logs.
	if attr, ok := resp.Schema.Attributes["credentials"]; ok && !attr.IsSensitive() {
		t.Error("credentials attribute must be marked sensitive")
	}
}

func TestProviderRegistersDataSources(t *testing.T) {
	p := New("test")()
	factories := p.DataSources(context.Background())
	got := map[string]bool{}
	for _, f := range factories {
		resp := &datasource.MetadataResponse{}
		f().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "admanager"}, resp)
		got[resp.TypeName] = true
	}
	for _, want := range []string{"admanager_network", "admanager_ad_unit", "admanager_ad_units"} {
		if !got[want] {
			t.Errorf("data source %q not registered; registered = %v", want, got)
		}
	}
}

func TestResolveConfigUsesExplicitValues(t *testing.T) {
	model := providerModel{
		NetworkCode:       types.StringValue("111"),
		Credentials:       types.StringValue("/path/to/sa.json"),
		RequestsPerSecond: types.Float64Value(4),
		RetryMaxAttempts:  types.Int64Value(7),
	}
	// Env var must lose to an explicit attribute.
	cfg, err := resolveConfig(model, func(string) string { return "999" })
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if cfg.NetworkCode != "111" || cfg.Credentials != "/path/to/sa.json" ||
		cfg.RequestsPerSecond != 4 || cfg.RetryMaxAttempts != 7 {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestResolveConfigFallsBackToEnv(t *testing.T) {
	model := providerModel{
		NetworkCode:       types.StringNull(),
		Credentials:       types.StringNull(),
		RequestsPerSecond: types.Float64Null(),
		RetryMaxAttempts:  types.Int64Null(),
	}
	getenv := func(key string) string {
		if key == "ADMANAGER_NETWORK_CODE" {
			return "222"
		}
		return ""
	}
	cfg, err := resolveConfig(model, getenv)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if cfg.NetworkCode != "222" {
		t.Errorf("NetworkCode = %q, want 222 (from env)", cfg.NetworkCode)
	}
	if cfg.Credentials != "" {
		t.Errorf("Credentials = %q, want empty (use ADC)", cfg.Credentials)
	}
	// Zero values let the client apply its own conservative defaults.
	if cfg.RequestsPerSecond != 0 || cfg.RetryMaxAttempts != 0 {
		t.Errorf("numeric knobs should stay zero when unset: %+v", cfg)
	}
}

func TestResolveConfigMissingNetworkCode(t *testing.T) {
	model := providerModel{
		NetworkCode:       types.StringNull(),
		Credentials:       types.StringNull(),
		RequestsPerSecond: types.Float64Null(),
		RetryMaxAttempts:  types.Int64Null(),
	}
	_, err := resolveConfig(model, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error when network code is missing everywhere, got nil")
	}
	// The error must tell the user both ways to fix it.
	if !strings.Contains(err.Error(), "network_code") ||
		!strings.Contains(err.Error(), "ADMANAGER_NETWORK_CODE") {
		t.Errorf("error not actionable: %v", err)
	}
}
