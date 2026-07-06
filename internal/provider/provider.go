// Package provider wires the Terraform Plugin Framework to the Ad Manager
// REST API client. Resources and data sources are registered here.
package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// envNetworkCode is the environment fallback for the network_code attribute.
const envNetworkCode = "ADMANAGER_NETWORK_CODE"

var _ provider.Provider = (*admanagerProvider)(nil)

type admanagerProvider struct {
	// version is "dev" for local builds and the release tag for published
	// binaries (injected by goreleaser via ldflags).
	version string
}

// New returns a provider factory, the shape expected by providerserver and
// by acceptance tests.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &admanagerProvider{version: version}
	}
}

// providerModel mirrors the provider {} configuration block.
type providerModel struct {
	NetworkCode       types.String  `tfsdk:"network_code"`
	Credentials       types.String  `tfsdk:"credentials"`
	RequestsPerSecond types.Float64 `tfsdk:"requests_per_second"`
	RetryMaxAttempts  types.Int64   `tfsdk:"retry_max_attempts"`
}

func (p *admanagerProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "admanager"
	resp.Version = p.version
}

func (p *admanagerProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage Google Ad Manager (GAM) configuration — ad units, placements, and " +
			"custom targeting — through the Ad Manager REST API (`admanager.googleapis.com`).\n\n" +
			"~> Most Ad Manager entities cannot be hard-deleted. `terraform destroy` **archives** (or " +
			"deactivates) entities instead of deleting them. See each resource's documentation.",
		Attributes: map[string]schema.Attribute{
			"network_code": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "The Ad Manager network code to manage (e.g. `123456`). " +
					"Can also be set with the `" + envNetworkCode + "` environment variable.",
			},
			"credentials": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Path to (or JSON content of) a Google service account key. The service " +
					"account must be added as a user of the Ad Manager network. When unset, " +
					"[Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials) " +
					"are used (which honor `GOOGLE_APPLICATION_CREDENTIALS`).",
			},
			"requests_per_second": schema.Float64Attribute{
				Optional: true,
				MarkdownDescription: "Client-side rate limit for Ad Manager API calls, shared by all resources. " +
					"Ad Manager quotas are low; the default of `2` is conservative on purpose. Raise it only if " +
					"your network's quota allows.",
			},
			"retry_max_attempts": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Maximum attempts per API call (initial call + retries) for rate-limit " +
					"and transient errors. Defaults to `5`.",
			},
		},
	}
}

// resolveConfig merges the provider block with environment fallbacks and
// validates that a network code is present. Zero values for the numeric knobs
// mean "let the client apply its defaults".
func resolveConfig(model providerModel, getenv func(string) string) (client.Config, error) {
	cfg := client.Config{
		NetworkCode:       model.NetworkCode.ValueString(),
		Credentials:       model.Credentials.ValueString(),
		RequestsPerSecond: model.RequestsPerSecond.ValueFloat64(),
		RetryMaxAttempts:  int(model.RetryMaxAttempts.ValueInt64()),
	}
	if cfg.NetworkCode == "" {
		cfg.NetworkCode = getenv(envNetworkCode)
	}
	if cfg.NetworkCode == "" {
		return client.Config{}, fmt.Errorf(
			"a network code is required: set the network_code attribute in the provider block or the %s environment variable",
			envNetworkCode,
		)
	}
	return cfg, nil
}

func (p *admanagerProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var model providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Values known only after apply (e.g. references to resources that do not
	// exist yet) cannot configure a working client.
	if model.NetworkCode.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("network_code"),
			"Unknown network code",
			"network_code depends on a value known only after apply. Use a static value or an environment variable.")
	}
	if model.Credentials.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("credentials"),
			"Unknown credentials",
			"credentials depends on a value known only after apply. Use a static value or Application Default Credentials.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	cfg, err := resolveConfig(model, os.Getenv)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	cfg.UserAgent = "terraform-provider-admanager/" + p.version

	c, err := client.New(ctx, cfg)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create Ad Manager API client", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *admanagerProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAdUnitResource,
		NewPlacementResource,
		NewCustomTargetingKeyResource,
	}
}

func (p *admanagerProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
