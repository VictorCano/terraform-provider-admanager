package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// Enum value sets sourced from the discovery document (rev 20260701). The
// UNSPECIFIED sentinels are intentionally excluded: they are never valid input.
var (
	targetWindowValues   = []string{"TOP", "BLANK"}
	smartSizeModeValues  = []string{"NONE", "SMART_BANNER", "DYNAMIC_SIZE"}
	sizeTypeValues       = []string{"PIXEL", "ASPECT_RATIO", "INTERSTITIAL", "IGNORED", "NATIVE", "FLUID", "AUDIO"}
	environmentTypeValue = []string{"BROWSER", "VIDEO_PLAYER"}
)

// Interface assertions.
var (
	_ resource.Resource                = (*adUnitResource)(nil)
	_ resource.ResourceWithConfigure   = (*adUnitResource)(nil)
	_ resource.ResourceWithImportState = (*adUnitResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*adUnitResource)(nil)
)

// NewAdUnitResource is the factory registered with the provider.
func NewAdUnitResource() resource.Resource {
	return &adUnitResource{}
}

type adUnitResource struct {
	client *client.Client
}

// adUnitModel is the shared Terraform view of an ad unit: every attribute the
// ad_unit resource and the admanager_ad_unit data source have in common. It is
// embedded by value into adUnitResourceModel (which adds the provider-side
// skip_archive_on_destroy) and into the data source model (which adds nothing).
// Embedding keeps a single source of truth for the field set and lets the
// shared adUnitModelFromAPI mapping serve both. Field ordering follows the
// schema. Lists of primitives use types.List so null and empty are
// distinguishable; nested blocks use typed slices for straightforward mapping.
type adUnitModel struct {
	ID                         types.String `tfsdk:"id"`
	AdUnitID                   types.String `tfsdk:"ad_unit_id"`
	ParentAdUnit               types.String `tfsdk:"parent_ad_unit"`
	DisplayName                types.String `tfsdk:"display_name"`
	AdUnitCode                 types.String `tfsdk:"ad_unit_code"`
	Description                types.String `tfsdk:"description"`
	TargetWindow               types.String `tfsdk:"target_window"`
	EffectiveTargetWindow      types.String `tfsdk:"effective_target_window"`
	ExplicitlyTargeted         types.Bool   `tfsdk:"explicitly_targeted"`
	AppliedAdsenseEnabled      types.Bool   `tfsdk:"applied_adsense_enabled"`
	EffectiveAdsenseEnabled    types.Bool   `tfsdk:"effective_adsense_enabled"`
	SmartSizeMode              types.String `tfsdk:"smart_size_mode"`
	RefreshDelay               types.String `tfsdk:"refresh_delay"`
	ExternalSetTopBoxChannelID types.String `tfsdk:"external_set_top_box_channel_id"`
	Status                     types.String `tfsdk:"status"`
	HasChildren                types.Bool   `tfsdk:"has_children"`
	UpdateTime                 types.String `tfsdk:"update_time"`
	Sizes                      []sizeModel  `tfsdk:"sizes"`
	AppliedTeams               types.List   `tfsdk:"applied_teams"`
	Teams                      types.List   `tfsdk:"teams"`

	// TODO(Fase 1+): applied_labels / effective_applied_labels and
	// applied_label_frequency_caps / effective_label_frequency_caps are part of
	// the API resource but are deferred. They reference Label resources that
	// this provider does not manage yet, and their applied/effective +
	// ordering semantics cannot be modeled with verified honest drift in this
	// pass. The client mirrors them; the schema deliberately omits them rather
	// than shipping half-working attributes with faked defaults.
}

// adUnitResourceModel is the resource's view of an ad unit: every shared
// attribute plus the provider-side-only skip_archive_on_destroy. Embedded
// fields are promoted, so existing accessors (m.DisplayName, m.ID, ...) are
// unchanged.
type adUnitResourceModel struct {
	adUnitModel
	SkipArchiveOnDestroy types.Bool `tfsdk:"skip_archive_on_destroy"`
}

// sizeModel mirrors one adUnitSizes entry.
type sizeModel struct {
	Width           types.Int64      `tfsdk:"width"`
	Height          types.Int64      `tfsdk:"height"`
	SizeType        types.String     `tfsdk:"size_type"`
	EnvironmentType types.String     `tfsdk:"environment_type"`
	Companions      []companionModel `tfsdk:"companions"`
}

// companionModel mirrors one companion Size (valid only for VIDEO_PLAYER).
type companionModel struct {
	Width    types.Int64  `tfsdk:"width"`
	Height   types.Int64  `tfsdk:"height"`
	SizeType types.String `tfsdk:"size_type"`
}

func (r *adUnitResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ad_unit"
}

func (r *adUnitResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	sizeSchema := schema.NestedAttributeObject{
		Attributes: map[string]schema.Attribute{
			"width": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Width of the size. For non-`PIXEL`/`ASPECT_RATIO` size types this must be `1`.",
			},
			"height": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Height of the size. For non-`PIXEL`/`ASPECT_RATIO` size types this must be `1`.",
			},
			"size_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The size type: one of `" + strings.Join(sizeTypeValues, "`, `") + "`.",
				Validators:          []validator.String{stringvalidator.OneOf(sizeTypeValues...)},
			},
			"environment_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The serving environment: `BROWSER` or `VIDEO_PLAYER`. Companions are only valid for `VIDEO_PLAYER`.",
				Validators:          []validator.String{stringvalidator.OneOf(environmentTypeValue...)},
			},
			"companions": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Companion sizes. Only valid when `environment_type` is `VIDEO_PLAYER`.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"width":  schema.Int64Attribute{Required: true, MarkdownDescription: "Width of the companion size."},
						"height": schema.Int64Attribute{Required: true, MarkdownDescription: "Height of the companion size."},
						"size_type": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "The companion size type: one of `" + strings.Join(sizeTypeValues, "`, `") + "`.",
							Validators:          []validator.String{stringvalidator.OneOf(sizeTypeValues...)},
						},
					},
				},
			},
		},
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Google Ad Manager [ad unit](https://developers.google.com/ad-manager/api/beta/reference/rest/v1/networks.adUnits). " +
			"Ad units are nodes in the network's inventory hierarchy.\n\n" +
			"~> **Destroy archives, it does not delete.** The Ad Manager API has no hard delete for ad units. " +
			"`terraform destroy` archives the ad unit via `adUnits:batchArchive`. Set `skip_archive_on_destroy = true` " +
			"to remove the ad unit from Terraform state without touching Ad Manager.\n\n" +
			"~> **An archived unit keeps its `ad_unit_code` reserved.** Because `parent_ad_unit` and `ad_unit_code` are " +
			"immutable, changing either forces replacement: Terraform destroys the current unit (archiving it) and creates " +
			"a new one. The archived unit still holds its `ad_unit_code`, so the replacement create reuses that code and " +
			"fails with `400 INVALID_ARGUMENT`. To replace a unit that sets `ad_unit_code`, either set " +
			"`skip_archive_on_destroy = true` on the current unit first (so it stays intact and can be re-adopted with " +
			"`terraform import`), give the replacement a different `ad_unit_code`, or unarchive and import the existing unit. " +
			"The provider warns about this during `plan`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The full resource name of the ad unit: `networks/{network_code}/adUnits/{ad_unit_id}`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ad_unit_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The numeric ad unit ID, parsed from the resource name for convenience.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"parent_ad_unit": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Resource name of the parent ad unit: `networks/{network_code}/adUnits/{ad_unit_id}`. " +
					"Use the network's `effectiveRootAdUnit` for top-level units. **Immutable**: changing it forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The display name of the ad unit (maximum 255 characters).",
			},
			"ad_unit_code": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "A string that uniquely identifies the ad unit for ad serving. **Immutable**. " +
					"If omitted, Ad Manager assigns one based on the ad unit ID. Changing it forces replacement.\n\n" +
					"An `ad_unit_code` stays **reserved by the ad unit that holds it even after that unit is archived**. " +
					"Because `terraform destroy` archives rather than deletes, replacing a unit that sets `ad_unit_code` " +
					"(by changing this value or another immutable attribute) archives the old unit and then fails to create " +
					"the replacement with the same code (`400 INVALID_ARGUMENT`). Use `skip_archive_on_destroy`, a different " +
					"code, or import-based recovery — see the resource notes above.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "A description of the ad unit (maximum 65,535 characters).",
			},
			"target_window": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "The target window applied directly to this ad unit: `TOP` (open in the full page body) " +
					"or `BLANK` (open in a new window). If unset, the ad unit inherits `effective_target_window`.",
				Validators: []validator.String{stringvalidator.OneOf(targetWindowValues...)},
			},
			"effective_target_window": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The resolved target window, inherited from ancestor ad units (defaults to `TOP`).",
			},
			"explicitly_targeted": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "If `true`, the ad unit is not implicitly targeted when its parent is; line items must " +
					"target it explicitly. Ad Manager 360 only. Defaults to `false`.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"applied_adsense_enabled": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "AdSense enablement applied directly to this ad unit. If unset, the value is inherited " +
					"(see `effective_adsense_enabled`).",
			},
			"effective_adsense_enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "The resolved AdSense enablement, inherited from ancestors when not set directly.",
			},
			"smart_size_mode": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The smart size mode: one of `" + strings.Join(smartSizeModeValues, "`, `") + "`. Defaults to `NONE` for fixed sizes.",
				Validators:          []validator.String{stringvalidator.OneOf(smartSizeModeValues...)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"refresh_delay": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "The duration after which the ad unit automatically refreshes, as a duration string " +
					"(e.g. `30s`). Valid only for ad units in mobile apps. If unset, the ad unit does not refresh.",
			},
			"external_set_top_box_channel_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The set-top-box video-on-demand channel this ad unit maps to in an external set-top-box ad system. Deprecated in the API.",
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
				Optional:            true,
				MarkdownDescription: "The sizes that can be served inside this ad unit.",
				NestedObject:        sizeSchema,
			},
			"applied_teams": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Resource names of Teams applied directly to this ad unit: " +
					"`networks/{network_code}/teams/{team_id}`.",
			},
			"teams": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Resource names of all Teams on this ad unit, including those inherited from ancestors.",
			},
			"skip_archive_on_destroy": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "When `true`, `terraform destroy` removes the ad unit from state without archiving it in " +
					"Ad Manager. Provider-side only; never sent to the API. Defaults to `false`.",
			},
		},
	}
}

func (r *adUnitResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // Provider not yet configured (e.g. during schema validation).
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected resource configure type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return
	}
	r.client = c
}

// numericIDFromName returns the trailing numeric id of a resource name. A bare
// id is returned unchanged; an empty string yields an empty string.
func numericIDFromName(name string) string {
	if name == "" {
		return ""
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// stringOrNull maps an API string to a Terraform value, treating "" as null so
// absent optional fields do not masquerade as empty strings.
func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// adUnitModelToAPI builds the API resource from the model, copying only the
// fields the API accepts on write. Output-only fields are never included.
func adUnitModelToAPI(ctx context.Context, m adUnitResourceModel) (*client.AdUnit, diag.Diagnostics) {
	var diags diag.Diagnostics
	au := &client.AdUnit{
		Name:         m.ID.ValueString(),
		DisplayName:  m.DisplayName.ValueString(),
		ParentAdUnit: m.ParentAdUnit.ValueString(),
	}
	if isSet(m.AdUnitCode) {
		au.AdUnitCode = m.AdUnitCode.ValueString()
	}
	if isSet(m.Description) {
		au.Description = m.Description.ValueString()
	}
	if isSet(m.TargetWindow) {
		au.AppliedTargetWindow = m.TargetWindow.ValueString()
	}
	if isSetBool(m.ExplicitlyTargeted) {
		v := m.ExplicitlyTargeted.ValueBool()
		au.ExplicitlyTargeted = &v
	}
	if isSetBool(m.AppliedAdsenseEnabled) {
		v := m.AppliedAdsenseEnabled.ValueBool()
		au.AppliedAdsenseEnabled = &v
	}
	if isSet(m.SmartSizeMode) {
		au.SmartSizeMode = m.SmartSizeMode.ValueString()
	}
	if isSet(m.RefreshDelay) {
		au.RefreshDelay = m.RefreshDelay.ValueString()
	}
	if isSet(m.ExternalSetTopBoxChannelID) {
		au.ExternalSetTopBoxChannelID = m.ExternalSetTopBoxChannelID.ValueString()
	}
	au.AdUnitSizes = sizesToAPI(m.Sizes)
	if !m.AppliedTeams.IsNull() && !m.AppliedTeams.IsUnknown() {
		var teams []string
		diags.Append(m.AppliedTeams.ElementsAs(ctx, &teams, false)...)
		au.AppliedTeams = teams
	}
	return au, diags
}

func isSet(v types.String) bool   { return !v.IsNull() && !v.IsUnknown() }
func isSetBool(v types.Bool) bool { return !v.IsNull() && !v.IsUnknown() }

func sizesToAPI(sizes []sizeModel) []client.AdUnitSize {
	if len(sizes) == 0 {
		return nil
	}
	out := make([]client.AdUnitSize, 0, len(sizes))
	for _, s := range sizes {
		as := client.AdUnitSize{
			Size: client.Size{
				Width:    s.Width.ValueInt64(),
				Height:   s.Height.ValueInt64(),
				SizeType: s.SizeType.ValueString(),
			},
			EnvironmentType: s.EnvironmentType.ValueString(),
		}
		for _, comp := range s.Companions {
			as.Companions = append(as.Companions, client.Size{
				Width:    comp.Width.ValueInt64(),
				Height:   comp.Height.ValueInt64(),
				SizeType: comp.SizeType.ValueString(),
			})
		}
		out = append(out, as)
	}
	return out
}

// adUnitModelFromAPI maps an API resource into the shared ad unit model,
// populating every attribute from what the API returned (honest drift). Both
// the resource (via adUnitAPIToModel) and the data source use it, so the
// null-handling and nested-block mapping live in exactly one place.
func adUnitModelFromAPI(ctx context.Context, au *client.AdUnit) (adUnitModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	m := adUnitModel{
		ID:                         types.StringValue(au.Name),
		AdUnitID:                   types.StringValue(adUnitNumericID(au)),
		ParentAdUnit:               types.StringValue(au.ParentAdUnit),
		DisplayName:                types.StringValue(au.DisplayName),
		AdUnitCode:                 stringOrNull(au.AdUnitCode),
		Description:                stringOrNull(au.Description),
		TargetWindow:               stringOrNull(au.AppliedTargetWindow),
		EffectiveTargetWindow:      stringOrNull(au.EffectiveTargetWindow),
		EffectiveAdsenseEnabled:    types.BoolValue(au.EffectiveAdsenseEnabled),
		SmartSizeMode:              stringOrNull(au.SmartSizeMode),
		RefreshDelay:               stringOrNull(au.RefreshDelay),
		ExternalSetTopBoxChannelID: stringOrNull(au.ExternalSetTopBoxChannelID),
		Status:                     stringOrNull(au.Status),
		HasChildren:                types.BoolValue(au.HasChildren),
		UpdateTime:                 stringOrNull(au.UpdateTime),
	}

	// explicitly_targeted is optional+computed: the effective value when unset
	// is the documented default (false), so map absent -> known false.
	if au.ExplicitlyTargeted != nil {
		m.ExplicitlyTargeted = types.BoolValue(*au.ExplicitlyTargeted)
	} else {
		m.ExplicitlyTargeted = types.BoolValue(false)
	}
	// applied_adsense_enabled is a plain optional: absent means "inherited", so
	// it must stay null rather than collapse to false.
	if au.AppliedAdsenseEnabled != nil {
		m.AppliedAdsenseEnabled = types.BoolValue(*au.AppliedAdsenseEnabled)
	} else {
		m.AppliedAdsenseEnabled = types.BoolNull()
	}

	m.Sizes = sizesFromAPI(au.AdUnitSizes)

	appliedTeams, d := stringSliceToList(ctx, au.AppliedTeams, false)
	diags.Append(d...)
	m.AppliedTeams = appliedTeams

	teams, d := stringSliceToList(ctx, au.Teams, true)
	diags.Append(d...)
	m.Teams = teams

	return m, diags
}

// adUnitAPIToModel maps an API resource into a full resource model. skipArchive
// is a provider-side-only value carried through unchanged from the prior
// plan/state.
func adUnitAPIToModel(ctx context.Context, au *client.AdUnit, skipArchive types.Bool) (adUnitResourceModel, diag.Diagnostics) {
	base, diags := adUnitModelFromAPI(ctx, au)
	return adUnitResourceModel{adUnitModel: base, SkipArchiveOnDestroy: skipArchive}, diags
}

// reconcileOmittedAppliedFields keeps a prior applied value ONLY when the API
// omitted the applied field from its response AND the effective twin corroborates
// that the prior value is the one actually in force. This handles old (often
// AdSense-linked) networks whose REST create/read responses echo the effective
// twin (effectiveTargetWindow / effectiveAdsenseEnabled) but omit the applied
// twin (appliedTargetWindow / appliedAdsenseEnabled) even though the write was
// accepted (issue #1). Without it the honest absent->null mapping contradicts the
// plan and Terraform rejects the apply with "inconsistent result after apply".
//
// This is honest drift, not diff suppression: the value is preserved only when it
// is observably applied via its effective twin. A genuine divergence — the API
// reports a different effective value, or there is no known prior value to
// corroborate — still surfaces as null / whatever the API returned, so real drift
// is never hidden. It is invoked from Create/Update against the plan value and
// from Read against the prior state value; the data source has no prior value and
// deliberately keeps the plain honest mapping.
//
// AUDIT (issue #1): smart_size_mode shares the same applied/effective inheritance
// shape but the API exposes NO effective twin for it — there is no
// effectiveSmartSizeMode field in the discovery doc (rev 20260701) — so an omitted
// smartSizeMode cannot be corroborated and its behavior is intentionally left
// unchanged here. That limitation is tracked in the PR's "deferred" notes.
func reconcileOmittedAppliedFields(m *adUnitModel, au *client.AdUnit, prior adUnitModel) {
	// target_window <- effectiveTargetWindow.
	if au.AppliedTargetWindow == "" && isSet(prior.TargetWindow) &&
		au.EffectiveTargetWindow == prior.TargetWindow.ValueString() {
		m.TargetWindow = prior.TargetWindow
	}
	// applied_adsense_enabled <- effectiveAdsenseEnabled (same corroborated
	// pattern; the effective twin is a plain output-only bool).
	if au.AppliedAdsenseEnabled == nil && isSetBool(prior.AppliedAdsenseEnabled) &&
		au.EffectiveAdsenseEnabled == prior.AppliedAdsenseEnabled.ValueBool() {
		m.AppliedAdsenseEnabled = prior.AppliedAdsenseEnabled
	}
}

// adUnitNumericID prefers the API-provided numeric id and falls back to parsing
// it out of the resource name.
func adUnitNumericID(au *client.AdUnit) string {
	if au.AdUnitID != "" {
		return au.AdUnitID
	}
	return numericIDFromName(au.Name)
}

func sizesFromAPI(sizes []client.AdUnitSize) []sizeModel {
	if len(sizes) == 0 {
		return nil
	}
	out := make([]sizeModel, 0, len(sizes))
	for _, s := range sizes {
		sm := sizeModel{
			Width:           types.Int64Value(s.Size.Width),
			Height:          types.Int64Value(s.Size.Height),
			SizeType:        stringOrNull(s.Size.SizeType),
			EnvironmentType: stringOrNull(s.EnvironmentType),
		}
		for _, comp := range s.Companions {
			sm.Companions = append(sm.Companions, companionModel{
				Width:    types.Int64Value(comp.Width),
				Height:   types.Int64Value(comp.Height),
				SizeType: stringOrNull(comp.SizeType),
			})
		}
		out = append(out, sm)
	}
	return out
}

// stringSliceToList converts an API string slice into a Terraform list. When
// the slice is empty, an optional attribute maps to null and a computed
// attribute maps to an empty list (both known, both honest for "none").
func stringSliceToList(ctx context.Context, values []string, computed bool) (types.List, diag.Diagnostics) {
	if len(values) == 0 {
		if computed {
			return types.ListValueMust(types.StringType, []attr.Value{}), nil
		}
		return types.ListNull(types.StringType), nil
	}
	return types.ListValueFrom(ctx, types.StringType, values)
}

// buildAdUnitUpdateMask returns the API field names whose settable values differ
// between plan and state. Immutable fields (parent_ad_unit, ad_unit_code) are
// never included; changing them forces replacement instead of a patch.
func buildAdUnitUpdateMask(plan, state *adUnitResourceModel) []string {
	var mask []string
	add := func(changed bool, field string) {
		if changed {
			mask = append(mask, field)
		}
	}
	add(!plan.DisplayName.Equal(state.DisplayName), "displayName")
	add(!plan.Description.Equal(state.Description), "description")
	add(!plan.TargetWindow.Equal(state.TargetWindow), "appliedTargetWindow")
	add(!plan.ExplicitlyTargeted.Equal(state.ExplicitlyTargeted), "explicitlyTargeted")
	add(!plan.AppliedAdsenseEnabled.Equal(state.AppliedAdsenseEnabled), "appliedAdsenseEnabled")
	add(!plan.SmartSizeMode.Equal(state.SmartSizeMode), "smartSizeMode")
	add(!plan.RefreshDelay.Equal(state.RefreshDelay), "refreshDelay")
	add(!plan.ExternalSetTopBoxChannelID.Equal(state.ExternalSetTopBoxChannelID), "externalSetTopBoxChannelId")
	add(!sizesEqual(plan.Sizes, state.Sizes), "adUnitSizes")
	add(!plan.AppliedTeams.Equal(state.AppliedTeams), "appliedTeams")
	return mask
}

func sizesEqual(a, b []sizeModel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Width.Equal(b[i].Width) || !a[i].Height.Equal(b[i].Height) ||
			!a[i].SizeType.Equal(b[i].SizeType) || !a[i].EnvironmentType.Equal(b[i].EnvironmentType) ||
			!companionsEqual(a[i].Companions, b[i].Companions) {
			return false
		}
	}
	return true
}

func companionsEqual(a, b []companionModel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Width.Equal(b[i].Width) || !a[i].Height.Equal(b[i].Height) ||
			!a[i].SizeType.Equal(b[i].SizeType) {
			return false
		}
	}
	return true
}

func (r *adUnitResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan adUnitResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	au, diags := adUnitModelToAPI(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// On create the resource name is unknown; the parent lives in the body.
	au.Name = ""

	created, err := r.client.CreateAdUnit(ctx, au)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create ad unit", r.createErrorDetail(ctx, err, plan))
		return
	}

	state, diags := adUnitAPIToModel(ctx, created, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Old-network read-back fallback: corroborate omitted applied fields against
	// the plan value (issue #1).
	reconcileOmittedAppliedFields(&state.adUnitModel, created, plan.adUnitModel)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *adUnitResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state adUnitResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	au, err := r.client.GetAdUnit(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			// The ad unit is gone from Ad Manager; drop it from state so a plan
			// proposes recreation instead of erroring.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read ad unit", apiErrorDetail("reading ad unit "+state.ID.ValueString(), err))
		return
	}

	newState, diags := adUnitAPIToModel(ctx, au, state.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Old-network read-back fallback: corroborate omitted applied fields against
	// the prior state value (issue #1).
	reconcileOmittedAppliedFields(&newState.adUnitModel, au, state.adUnitModel)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *adUnitResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state adUnitResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	mask := buildAdUnitUpdateMask(&plan, &state)
	if len(mask) == 0 {
		// Nothing patchable changed (e.g. only skip_archive_on_destroy, which
		// never reaches the API). The framework marks null-config computed
		// attributes (effective_*, status, has_children, update_time, teams)
		// Unknown in the plan, so persisting `plan` would write unknown values
		// into post-apply state and Terraform would reject the result. Carry the
		// full prior state forward — its computed values are the known, correct
		// ones — and apply only the provider-side skip_archive_on_destroy, which
		// is the sole attribute that can differ when the mask is empty.
		state.SkipArchiveOnDestroy = plan.SkipArchiveOnDestroy
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	au, diags := adUnitModelToAPI(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	au.Name = state.ID.ValueString() // The name is immutable; use the known one.

	updated, err := r.client.PatchAdUnit(ctx, au, mask)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update ad unit", apiErrorDetail("updating ad unit "+au.Name, err))
		return
	}

	newState, diags := adUnitAPIToModel(ctx, updated, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Old-network read-back fallback: corroborate omitted applied fields against
	// the plan value (issue #1).
	reconcileOmittedAppliedFields(&newState.adUnitModel, updated, plan.adUnitModel)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *adUnitResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state adUnitResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.SkipArchiveOnDestroy.ValueBool() {
		// Provider-side opt-out: forget the resource without touching Ad Manager.
		return
	}

	name := state.ID.ValueString()
	err := r.client.ArchiveAdUnit(ctx, name)
	if err == nil {
		return
	}
	if client.IsNotFound(err) {
		// Already gone: nothing to archive. Treat as success.
		resp.Diagnostics.AddWarning(
			"Ad unit already absent",
			fmt.Sprintf("Ad unit %s was not found while archiving; it is treated as already destroyed.", name),
		)
		return
	}
	// The archive failed for some other reason. The batchArchive error alone
	// cannot tell "already archived" apart from a genuine block (e.g. the unit
	// still has active children or line items) — both commonly surface as
	// FAILED_PRECONDITION and/or a message mentioning "archive". Re-read the ad
	// unit and only tolerate the failure when it actually reads back ARCHIVED
	// (or is gone). Anything else is surfaced so the resource stays in state
	// instead of being silently dropped while still live in Ad Manager.
	if archived, verifyErr := r.adUnitIsArchived(ctx, name); verifyErr == nil && archived {
		resp.Diagnostics.AddWarning(
			"Ad unit already archived",
			fmt.Sprintf("Ad unit %s was already archived in Ad Manager; no action was taken.", name),
		)
		return
	}
	resp.Diagnostics.AddError("Unable to archive ad unit", apiErrorDetail("archiving ad unit "+name, err))
}

// adUnitIsArchived reports whether the ad unit currently reads back as ARCHIVED.
// A 404 counts as archived-or-gone: the unit is no longer active, so destroy can
// safely drop it from state. Any other read error is returned so the caller
// surfaces the original archive failure rather than dropping a live unit.
func (r *adUnitResource) adUnitIsArchived(ctx context.Context, name string) (bool, error) {
	au, err := r.client.GetAdUnit(ctx, name)
	if err != nil {
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return au.Status == "ARCHIVED", nil
}

func (r *adUnitResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name := normalizeAdUnitName(strings.TrimSpace(req.ID), r.client.NetworkCode())
	if name == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be a full resource name (networks/{network_code}/adUnits/{ad_unit_id}) or a bare numeric ad unit ID.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
}

// ModifyPlan warns, at plan time, about the issue #2 reserved-code trap: when a
// change forces this ad unit to be replaced, Terraform destroys the current unit
// (which archives it — the API has no hard delete) and creates a new one. An
// archived ad unit keeps its immutable ad_unit_code reserved, so the replacement
// create reuses that code and fails with 400 INVALID_ARGUMENT, leaving the
// original unit archived. Surfacing this during plan lets the operator choose a
// non-destructive path before applying. It makes NO API calls — it is a pure
// predicate over prior state and planned values.
func (r *adUnitResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Only a replace of an existing unit can hit the trap. Create has no prior
	// state; destroy has a null plan. Both are out.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan adUnitResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !adUnitReplaceReservesCode(state, plan, len(resp.RequiresReplace) > 0) {
		return
	}
	resp.Diagnostics.AddWarning(
		"Replacing this ad unit archives it and reserves its ad_unit_code",
		fmt.Sprintf(
			"This change forces ad unit %s to be replaced: Terraform will destroy the current unit and "+
				"create a new one. Destroy archives the unit (the Ad Manager API has no hard delete), and an "+
				"archived ad unit keeps its ad_unit_code %q reserved. The replacement create reuses that same "+
				"ad_unit_code and will fail with HTTP 400 INVALID_ARGUMENT because the code is still held by the "+
				"archived unit, leaving the original unit archived.\n\n"+
				"To avoid this, do one of:\n"+
				"  - Set skip_archive_on_destroy = true on the current unit and apply that first, so destroy "+
				"leaves it intact in Ad Manager; you can then re-adopt it with terraform import instead of losing it.\n"+
				"  - Give the replacement a different ad_unit_code so its create does not collide.\n"+
				"  - Unarchive the existing unit in Ad Manager and use terraform import to adopt it instead of replacing it.",
			state.ID.ValueString(), state.AdUnitCode.ValueString()),
	)
}

// adUnitReplaceReservesCode reports whether the planned change is a replace whose
// replacement create will collide on a reserved ad_unit_code — the issue #2 trap.
// It is a pure predicate (no API calls): a replace is detected either from a
// differing immutable attribute (parent_ad_unit or ad_unit_code) or from the
// framework already flagging RequiresReplace. skip_archive_on_destroy short-
// circuits it, because then destroy touches nothing in Ad Manager and no code is
// reserved.
//
// Detecting a replace is necessary but NOT sufficient: the collision only occurs
// when the replacement create SENDS the same ad_unit_code the archived unit still
// holds. adUnitModelToAPI sends plan.AdUnitCode only when it isSet (known and
// non-null); an unknown or null planned code is omitted and GAM auto-assigns a
// fresh, non-colliding one. So the trap requires the planned code to be known and
// equal to the prior code:
//   - Renaming ad_unit_code (old -> new) is a RequiresReplace, but the create
//     sends the new, unreserved code and SUCCEEDS — not the trap.
//   - Changing another immutable (e.g. parent_ad_unit) while ad_unit_code stays
//     the same makes the create reuse the reserved code — the real trap. This
//     also covers an auto-assigned code that UseStateForUnknown retains in the
//     plan (plan == prior code), so it too warns.
func adUnitReplaceReservesCode(state, plan adUnitResourceModel, frameworkRequiresReplace bool) bool {
	isReplace := frameworkRequiresReplace ||
		!state.ParentAdUnit.Equal(plan.ParentAdUnit) ||
		!state.AdUnitCode.Equal(plan.AdUnitCode)
	if !isReplace {
		return false
	}
	// The destroy leg governs archiving, and it reads the prior state's opt-out.
	if state.SkipArchiveOnDestroy.ValueBool() {
		return false
	}
	// No code on the prior unit means nothing gets reserved.
	if !isSet(state.AdUnitCode) || state.AdUnitCode.ValueString() == "" {
		return false
	}
	// The collision only fires when the replacement create reuses the exact code
	// the archived unit holds: the planned code must be known and equal to the
	// prior code. A changed code (rename) or an omitted/unknown code cannot
	// collide.
	return isSet(plan.AdUnitCode) && plan.AdUnitCode.ValueString() == state.AdUnitCode.ValueString()
}

// normalizeAdUnitName accepts either a full resource name or a bare numeric id
// and returns a full resource name scoped to networkCode.
func normalizeAdUnitName(id, networkCode string) string {
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "networks/") {
		return id
	}
	// A bare id (no slashes) is expanded against the configured network.
	if !strings.Contains(id, "/") {
		return fmt.Sprintf("networks/%s/adUnits/%s", networkCode, id)
	}
	// Anything else contains a slash but is not a full resource name (e.g.
	// "adUnits/123" or a "network/..." typo). It is malformed: return empty so
	// ImportState emits the "Invalid import ID" diagnostic instead of passing a
	// bad resource name straight to the API.
	return ""
}

// createErrorDetail renders the Create failure diagnostic. The base is always
// the detail-enriched API error. When the API rejects the create with 400
// INVALID_ARGUMENT and the plan pinned an ad_unit_code, it additionally does a
// best-effort lookup of who currently holds that code (archived units included)
// and, if a holder is found, appends the holder identity and the concrete
// recovery paths. This diagnoses the issue #2 trap where a replace archives the
// prior unit, the archived unit keeps its immutable ad_unit_code reserved, and
// the follow-up create then fails on the reserved code with only the opaque
// top-level message.
//
// The lookup is best effort: any failure (or no match) falls back to the base
// diagnostic so a flaky list call can never mask the real create error.
func (r *adUnitResource) createErrorDetail(ctx context.Context, createErr error, plan adUnitResourceModel) string {
	base := apiErrorDetail("creating ad unit", createErr)
	if !isSet(plan.AdUnitCode) {
		return base
	}
	var apiErr *client.APIError
	if !errors.As(createErr, &apiErr) ||
		apiErr.StatusCode != http.StatusBadRequest || apiErr.Status != "INVALID_ARGUMENT" {
		return base
	}
	code := plan.AdUnitCode.ValueString()
	holder, ok := r.findAdUnitCodeHolder(ctx, code)
	if !ok {
		return base
	}
	holderDesc := holder.Name
	if holder.DisplayName != "" {
		holderDesc = fmt.Sprintf("%q (%s)", holder.DisplayName, holder.Name)
	}
	return base + "\n\n" + fmt.Sprintf(
		"ad_unit_code %q is already held by %s (status %s). "+
			"Unarchive it and use terraform import, or choose a different ad_unit_code.",
		code, holderDesc, holder.Status)
}

// findAdUnitCodeHolder best-effort looks up the ad unit that currently holds
// code, via a server-side list filter. The filter constrains only adUnitCode and
// adds NO status clause, so an ARCHIVED holder (the issue #2 case) is included —
// the API returns units of every status unless a status filter excludes them.
// The lookup goes through the client (and therefore the rate limiter). It returns
// (nil, false) on any error or no match; the caller then falls back to the base
// diagnostic rather than surfacing the lookup failure.
func (r *adUnitResource) findAdUnitCodeHolder(ctx context.Context, code string) (*client.AdUnit, bool) {
	filter := fmt.Sprintf(`adUnitCode = "%s"`, escapeFilterString(code))
	units, err := r.client.ListAdUnits(ctx, client.ListAdUnitsOptions{
		Filter:   filter,
		PageSize: adUnitListPageSize,
	})
	if err != nil || len(units) == 0 {
		return nil, false
	}
	return &units[0], true
}

// apiErrorDetail renders an actionable diagnostic detail: the operation plus,
// when available, the API status and message. It never includes credentials —
// APIError only carries what the API returned.
func apiErrorDetail(operation string, err error) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status != "" {
			return fmt.Sprintf("While %s, the Ad Manager API returned HTTP %d (%s): %s",
				operation, apiErr.StatusCode, apiErr.Status, apiErr.Message)
		}
		return fmt.Sprintf("While %s, the Ad Manager API returned HTTP %d: %s",
			operation, apiErr.StatusCode, apiErr.Message)
	}
	return fmt.Sprintf("While %s: %s", operation, err.Error())
}
