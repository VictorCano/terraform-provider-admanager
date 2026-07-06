package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// adUnitListPageSize requests the API's maximum page size (1000) for list-backed
// reads. Larger pages mean fewer round trips through the rate limiter: the
// by-code lookup stays a single request, and admanager_ad_units fetches large
// result sets in as few pages as the API allows.
const adUnitListPageSize = 1000

// Interface assertions.
var (
	_ datasource.DataSource                     = (*adUnitDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*adUnitDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*adUnitDataSource)(nil)
)

// NewAdUnitDataSource is the factory registered with the provider.
func NewAdUnitDataSource() datasource.DataSource {
	return &adUnitDataSource{}
}

type adUnitDataSource struct {
	client *client.Client
}

// adUnitDataSourceModel is the data source's view of an ad unit: the full shared
// attribute set (adUnitModel) and nothing else. ad_unit_id and ad_unit_code
// double as the lookup inputs and as outputs.
type adUnitDataSourceModel struct {
	adUnitModel
}

func (d *adUnitDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ad_unit"
}

func (d *adUnitDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	sizeSchema := schema.NestedAttributeObject{
		Attributes: map[string]schema.Attribute{
			"width":  schema.Int64Attribute{Computed: true, MarkdownDescription: "Width of the size."},
			"height": schema.Int64Attribute{Computed: true, MarkdownDescription: "Height of the size."},
			"size_type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The size type: one of `" + strings.Join(sizeTypeValues, "`, `") + "`.",
			},
			"environment_type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The serving environment: `BROWSER` or `VIDEO_PLAYER`.",
			},
			"companions": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Companion sizes, present only for `VIDEO_PLAYER` sizes.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"width":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Width of the companion size."},
						"height":    schema.Int64Attribute{Computed: true, MarkdownDescription: "Height of the companion size."},
						"size_type": schema.StringAttribute{Computed: true, MarkdownDescription: "The companion size type."},
					},
				},
			},
		},
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a single Google Ad Manager [ad unit](https://developers.google.com/ad-manager/api/beta/reference/rest/v1/networks.adUnits) " +
			"by either `ad_unit_id` or `ad_unit_code`. Exactly one of the two must be set.",
		Attributes: map[string]schema.Attribute{
			"ad_unit_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Look up the ad unit by ID: either a bare numeric ad unit ID (expanded against the " +
					"configured network) or a full resource name `networks/{network_code}/adUnits/{ad_unit_id}`. " +
					"Exactly one of `ad_unit_id` or `ad_unit_code` must be set.",
			},
			"ad_unit_code": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Look up the ad unit by its exact ad serving code. Exactly one of `ad_unit_id` or " +
					"`ad_unit_code` must be set.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The full resource name of the ad unit: `networks/{network_code}/adUnits/{ad_unit_id}`.",
			},
			"parent_ad_unit": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource name of the parent ad unit: `networks/{network_code}/adUnits/{ad_unit_id}`. Null for the root ad unit.",
			},
			"display_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The display name of the ad unit.",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "A description of the ad unit.",
			},
			"target_window": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The target window applied directly to this ad unit (`TOP` or `BLANK`). Null when inherited.",
			},
			"effective_target_window": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The resolved target window, inherited from ancestor ad units.",
			},
			"explicitly_targeted": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the ad unit must be targeted explicitly by line items. Ad Manager 360 only.",
			},
			"applied_adsense_enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "AdSense enablement applied directly to this ad unit. Null when inherited.",
			},
			"effective_adsense_enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "The resolved AdSense enablement, inherited from ancestors when not set directly.",
			},
			"smart_size_mode": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The smart size mode: one of `" + strings.Join(smartSizeModeValues, "`, `") + "`.",
			},
			"refresh_delay": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The auto-refresh duration (e.g. `30s`) for mobile-app ad units. Null when the ad unit does not refresh.",
			},
			"external_set_top_box_channel_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The set-top-box video-on-demand channel this ad unit maps to. Deprecated in the API.",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The status of the ad unit: `ACTIVE`, `INACTIVE`, or `ARCHIVED`.",
			},
			"has_children": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the ad unit has any child ad units.",
			},
			"update_time": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The time the ad unit was last modified (RFC 3339).",
			},
			"sizes": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The sizes that can be served inside this ad unit.",
				NestedObject:        sizeSchema,
			},
			"applied_teams": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Resource names of Teams applied directly to this ad unit.",
			},
			"teams": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Resource names of all Teams on this ad unit, including those inherited from ancestors.",
			},
		},
	}
}

func (d *adUnitDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		// Exactly one lookup key: neither-set or both-set is a config error caught
		// at plan time, before any API call.
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("ad_unit_id"),
			path.MatchRoot("ad_unit_code"),
		),
	}
}

func (d *adUnitDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
}

// escapeFilterString escapes a value for embedding inside a double-quoted
// AIP-160 filter string literal, so a code containing a quote or backslash
// cannot break out of the literal or alter the filter expression.
func escapeFilterString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// resolveAdUnit fetches the single ad unit identified by exactly one of adUnitID
// or adUnitCode. The ExactlyOneOf config validator guarantees the caller sets
// exactly one; the default branch is defensive.
func (d *adUnitDataSource) resolveAdUnit(ctx context.Context, adUnitID, adUnitCode types.String) (*client.AdUnit, diag.Diagnostics) {
	var diags diag.Diagnostics
	switch {
	case isSet(adUnitID):
		name := normalizeAdUnitName(strings.TrimSpace(adUnitID.ValueString()), d.client.NetworkCode())
		if name == "" {
			diags.AddAttributeError(
				path.Root("ad_unit_id"),
				"Invalid ad_unit_id",
				"ad_unit_id must be a full resource name (networks/{network_code}/adUnits/{ad_unit_id}) or a bare numeric ad unit ID.",
			)
			return nil, diags
		}
		au, err := d.client.GetAdUnit(ctx, name)
		if err != nil {
			if client.IsNotFound(err) {
				diags.AddError(
					"Ad unit not found",
					fmt.Sprintf("No ad unit was found for ad_unit_id %q (resolved to %s).", adUnitID.ValueString(), name),
				)
				return nil, diags
			}
			diags.AddError("Unable to read ad unit", apiErrorDetail("reading ad unit "+name, err))
			return nil, diags
		}
		return au, diags
	case isSet(adUnitCode):
		return d.lookupAdUnitByCode(ctx, adUnitCode.ValueString())
	default:
		diags.AddError(
			"Missing lookup argument",
			"Exactly one of ad_unit_id or ad_unit_code must be set to look up an ad unit.",
		)
		return nil, diags
	}
}

// lookupAdUnitByCode finds the ad unit whose adUnitCode exactly equals code via
// a server-side list filter. Ad unit codes are unique, so a well-formed network
// returns zero or one match; more than one is surfaced as an error rather than
// silently picking one.
func (d *adUnitDataSource) lookupAdUnitByCode(ctx context.Context, code string) (*client.AdUnit, diag.Diagnostics) {
	var diags diag.Diagnostics
	filter := fmt.Sprintf(`adUnitCode = "%s"`, escapeFilterString(code))
	units, err := d.client.ListAdUnits(ctx, client.ListAdUnitsOptions{
		Filter:   filter,
		PageSize: adUnitListPageSize,
	})
	if err != nil {
		diags.AddError("Unable to look up ad unit by code", apiErrorDetail("listing ad units with filter "+filter, err))
		return nil, diags
	}
	switch len(units) {
	case 0:
		diags.AddError(
			"Ad unit not found",
			fmt.Sprintf("No ad unit was found with ad_unit_code %q.", code),
		)
		return nil, diags
	case 1:
		return &units[0], diags
	default:
		diags.AddError(
			"Multiple ad units matched",
			fmt.Sprintf("ad_unit_code %q matched %d ad units; expected exactly one. Ad unit codes are meant to be unique — "+
				"inspect the network for duplicates.", code, len(units)),
		)
		return nil, diags
	}
}

// adUnitDataSourceModelFromAPI maps an API resource into the data source model,
// reusing the shared adUnitModelFromAPI mapping so null-handling and nested
// blocks match the resource exactly.
func adUnitDataSourceModelFromAPI(ctx context.Context, au *client.AdUnit) (adUnitDataSourceModel, diag.Diagnostics) {
	base, diags := adUnitModelFromAPI(ctx, au)
	return adUnitDataSourceModel{adUnitModel: base}, diags
}

func (d *adUnitDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config adUnitDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	au, diags := d.resolveAdUnit(ctx, config.AdUnitID, config.AdUnitCode)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := adUnitDataSourceModelFromAPI(ctx, au)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
