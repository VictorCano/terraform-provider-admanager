package provider

import (
	"context"
	"errors"
	"fmt"
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
)

// NewAdUnitResource is the factory registered with the provider.
func NewAdUnitResource() resource.Resource {
	return &adUnitResource{}
}

type adUnitResource struct {
	client *client.Client
}

// adUnitResourceModel is the Terraform view of an ad unit. Field ordering
// follows the schema. Lists of primitives use types.List so null and empty are
// distinguishable; nested blocks use typed slices for straightforward mapping.
type adUnitResourceModel struct {
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
	SkipArchiveOnDestroy       types.Bool   `tfsdk:"skip_archive_on_destroy"`

	// TODO(Fase 1+): applied_labels / effective_applied_labels and
	// applied_label_frequency_caps / effective_label_frequency_caps are part of
	// the API resource but are deferred. They reference Label resources that
	// this provider does not manage yet, and their applied/effective +
	// ordering semantics cannot be modeled with verified honest drift in this
	// pass. The client mirrors them; the schema deliberately omits them rather
	// than shipping half-working attributes with faked defaults.
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
			"to remove the ad unit from Terraform state without touching Ad Manager.",
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
					"If omitted, Ad Manager assigns one based on the ad unit ID. Changing it forces replacement.",
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

// adUnitAPIToModel maps an API resource into a full Terraform model, populating
// every attribute from what the API returned (honest drift). skipArchive is a
// provider-side-only value carried through unchanged from the prior plan/state.
func adUnitAPIToModel(ctx context.Context, au *client.AdUnit, skipArchive types.Bool) (adUnitResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	m := adUnitResourceModel{
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
		SkipArchiveOnDestroy:       skipArchive,
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
		resp.Diagnostics.AddError("Unable to create ad unit", apiErrorDetail("creating ad unit", err))
		return
	}

	state, diags := adUnitAPIToModel(ctx, created, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
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
