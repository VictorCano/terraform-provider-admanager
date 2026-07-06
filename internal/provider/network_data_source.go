package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// Interface assertions.
var (
	_ datasource.DataSource              = (*networkDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*networkDataSource)(nil)
)

// NewNetworkDataSource is the factory registered with the provider.
func NewNetworkDataSource() datasource.DataSource {
	return &networkDataSource{}
}

type networkDataSource struct {
	client *client.Client
}

// networkDataSourceModel mirrors the Ad Manager Network resource. Every field is
// output-only in the API, so every attribute is computed. It takes no arguments:
// the network read is the one the provider is configured for.
type networkDataSourceModel struct {
	ID                     types.String `tfsdk:"id"`
	NetworkCode            types.String `tfsdk:"network_code"`
	DisplayName            types.String `tfsdk:"display_name"`
	TimeZone               types.String `tfsdk:"time_zone"`
	CurrencyCode           types.String `tfsdk:"currency_code"`
	SecondaryCurrencyCodes types.List   `tfsdk:"secondary_currency_codes"`
	EffectiveRootAdUnit    types.String `tfsdk:"effective_root_ad_unit"`
	NetworkID              types.String `tfsdk:"network_id"`
	PropertyCode           types.String `tfsdk:"property_code"`
	TestNetwork            types.Bool   `tfsdk:"test_network"`
}

func (d *networkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (d *networkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the Google Ad Manager [network](https://developers.google.com/ad-manager/api/beta/reference/rest/v1/networks) " +
			"the provider is configured for (via `network_code` or the `" + envNetworkCode + "` environment variable). " +
			"Takes no arguments; every attribute is read from the API. A common use is looking up `effective_root_ad_unit` " +
			"to parent top-level ad units.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The full resource name of the network: `networks/{network_code}`.",
			},
			"network_code": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The network code.",
			},
			"display_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The display name of the network.",
			},
			"time_zone": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The time zone associated with the delivery of orders and reporting (e.g. `America/Sao_Paulo`).",
			},
			"currency_code": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The primary currency code, in ISO 4217 format (e.g. `USD`).",
			},
			"secondary_currency_codes": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Currency codes that can be used as an alternative to the primary currency. Empty when the network has none.",
			},
			"effective_root_ad_unit": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The resource name of the top-most ad unit, to which all other ad units descend: " +
					"`networks/{network_code}/adUnits/{ad_unit_id}`. Use it as the `parent_ad_unit` of a top-level `admanager_ad_unit`.",
			},
			"network_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The numeric network ID.",
			},
			"property_code": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The property code. Null when the network has none.",
			},
			"test_network": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this is a test network. Acceptance tests refuse to run against a network where this is `false`.",
			},
		},
	}
}

func (d *networkDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
}

// networkAPIToModel maps an API Network into the data source model, reflecting
// exactly what the API returned: absent strings become null (not empty), and the
// secondary currency list is a known empty list when the API omits it.
func networkAPIToModel(ctx context.Context, n *client.Network) (networkDataSourceModel, diag.Diagnostics) {
	secondary, diags := stringSliceToList(ctx, n.SecondaryCurrencyCodes, true)
	return networkDataSourceModel{
		ID:                     stringOrNull(n.Name),
		NetworkCode:            stringOrNull(n.NetworkCode),
		DisplayName:            stringOrNull(n.DisplayName),
		TimeZone:               stringOrNull(n.TimeZone),
		CurrencyCode:           stringOrNull(n.CurrencyCode),
		SecondaryCurrencyCodes: secondary,
		EffectiveRootAdUnit:    stringOrNull(n.EffectiveRootAdUnit),
		NetworkID:              stringOrNull(n.NetworkID),
		PropertyCode:           stringOrNull(n.PropertyCode),
		TestNetwork:            types.BoolValue(n.TestNetwork),
	}, diags
}

func (d *networkDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	net, err := d.client.GetNetwork(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read network", apiErrorDetail("reading network", err))
		return
	}

	state, diags := networkAPIToModel(ctx, net)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
