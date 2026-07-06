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
	_ datasource.DataSource              = (*adUnitsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*adUnitsDataSource)(nil)
)

// NewAdUnitsDataSource is the factory registered with the provider.
func NewAdUnitsDataSource() datasource.DataSource {
	return &adUnitsDataSource{}
}

type adUnitsDataSource struct {
	client *client.Client
}

// adUnitsDataSourceModel is the plural data source: an optional filter in, a
// list of matching ad unit summaries out.
type adUnitsDataSourceModel struct {
	Filter  types.String         `tfsdk:"filter"`
	AdUnits []adUnitSummaryModel `tfsdk:"ad_units"`
}

// adUnitSummaryModel is the compact per-unit projection returned by the list.
// It is deliberately a small subset of the full ad unit: enough to identify and
// navigate the inventory tree without paying to model every field for every
// unit. Callers who need the full attribute set look a unit up with the
// singular admanager_ad_unit data source.
type adUnitSummaryModel struct {
	ID           types.String `tfsdk:"id"`
	AdUnitID     types.String `tfsdk:"ad_unit_id"`
	ParentAdUnit types.String `tfsdk:"parent_ad_unit"`
	DisplayName  types.String `tfsdk:"display_name"`
	AdUnitCode   types.String `tfsdk:"ad_unit_code"`
	Status       types.String `tfsdk:"status"`
}

func (d *adUnitsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ad_units"
}

func (d *adUnitsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists Google Ad Manager [ad units](https://developers.google.com/ad-manager/api/beta/reference/rest/v1/networks.adUnits) " +
			"in the configured network, optionally narrowed by a `filter`.",
		Attributes: map[string]schema.Attribute{
			"filter": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "An [AIP-160 filter expression](https://developers.google.com/ad-manager/api/beta/filters) " +
					"passed through to the API's `filter` parameter. Filterable fields include `adUnitCode`, `displayName`, " +
					"`parentAdUnit`, `status`, `hasChildren`, and `explicitlyTargeted`. Use `*` for wildcard matching " +
					"(e.g. `displayName = \"Homepage*\"`); the `like` operator is **not** supported. When omitted, every ad " +
					"unit in the network is returned.",
			},
			"ad_units": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The ad units matching the filter (all of them when `filter` is omitted).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The full resource name of the ad unit: `networks/{network_code}/adUnits/{ad_unit_id}`.",
						},
						"ad_unit_id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The numeric ad unit ID.",
						},
						"parent_ad_unit": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Resource name of the parent ad unit. Null for the root ad unit.",
						},
						"display_name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The display name of the ad unit.",
						},
						"ad_unit_code": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The ad serving code of the ad unit.",
						},
						"status": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "The status of the ad unit: `ACTIVE`, `INACTIVE`, or `ARCHIVED`.",
						},
					},
				},
			},
		},
	}
}

func (d *adUnitsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
}

// adUnitSummariesFromAPI projects API ad units into the compact summary model.
// Absent parent/code (e.g. on the root ad unit) map to null, not empty strings.
// It always returns a non-nil slice so a zero-match result serializes as an
// empty list `[]` (honest "matched none", and iterable in a `for` expression),
// never null.
func adUnitSummariesFromAPI(units []client.AdUnit) []adUnitSummaryModel {
	out := make([]adUnitSummaryModel, 0, len(units))
	for i := range units {
		au := &units[i]
		out = append(out, adUnitSummaryModel{
			ID:           types.StringValue(au.Name),
			AdUnitID:     types.StringValue(adUnitNumericID(au)),
			ParentAdUnit: stringOrNull(au.ParentAdUnit),
			DisplayName:  stringOrNull(au.DisplayName),
			AdUnitCode:   stringOrNull(au.AdUnitCode),
			Status:       stringOrNull(au.Status),
		})
	}
	return out
}

// listSummaries fetches all matching ad units (following pagination inside the
// client) and projects them to summaries. filter is passed through only when set.
func (d *adUnitsDataSource) listSummaries(ctx context.Context, filter types.String) ([]adUnitSummaryModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	opts := client.ListAdUnitsOptions{PageSize: adUnitListPageSize}
	if isSet(filter) {
		opts.Filter = filter.ValueString()
	}
	units, err := d.client.ListAdUnits(ctx, opts)
	if err != nil {
		diags.AddError("Unable to list ad units", apiErrorDetail("listing ad units", err))
		return nil, diags
	}
	return adUnitSummariesFromAPI(units), diags
}

func (d *adUnitsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config adUnitsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	summaries, diags := d.listSummaries(ctx, config.Filter)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.AdUnits = summaries
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
