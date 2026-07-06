package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// Interface assertions.
var (
	_ resource.Resource                = (*placementResource)(nil)
	_ resource.ResourceWithConfigure   = (*placementResource)(nil)
	_ resource.ResourceWithImportState = (*placementResource)(nil)
)

// NewPlacementResource is the factory registered with the provider.
func NewPlacementResource() resource.Resource {
	return &placementResource{}
}

type placementResource struct {
	client *client.Client
}

// placementResourceModel is the Terraform view of a placement. targeted_ad_units
// uses types.Set because the API treats the ad units as an unordered collection:
// membership, not order, is significant. A set makes equality order-insensitive,
// so an API-side reordering never shows up as spurious drift. Null (the attribute
// omitted) stays distinct from a populated set; an explicit empty set is rejected
// by a validator (see the schema), since the API cannot represent "empty" apart
// from "absent" and would read it back as null.
type placementResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	PlacementID          types.String `tfsdk:"placement_id"`
	DisplayName          types.String `tfsdk:"display_name"`
	Description          types.String `tfsdk:"description"`
	PlacementCode        types.String `tfsdk:"placement_code"`
	TargetedAdUnits      types.Set    `tfsdk:"targeted_ad_units"`
	Status               types.String `tfsdk:"status"`
	UpdateTime           types.String `tfsdk:"update_time"`
	SkipArchiveOnDestroy types.Bool   `tfsdk:"skip_archive_on_destroy"`
}

func (r *placementResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_placement"
}

func (r *placementResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Google Ad Manager [placement](https://developers.google.com/ad-manager/api/beta/reference/rest/v1/networks.placements). " +
			"A placement groups ad units so they can be targeted together.\n\n" +
			"~> **Destroy archives, it does not delete.** The Ad Manager API has no hard delete for placements. " +
			"`terraform destroy` archives the placement via `placements:batchArchive`. Set `skip_archive_on_destroy = true` " +
			"to remove the placement from Terraform state without touching Ad Manager.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The full resource name of the placement: `networks/{network_code}/placements/{placement_id}`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"placement_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The numeric placement ID, parsed from the resource name for convenience.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The display name of the placement (maximum 255 characters).",
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "A description of the placement (maximum 65,535 characters).",
			},
			"placement_code": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "A string that uniquely identifies the placement for ad serving. " +
					"Assigned by Google; it cannot be set.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"targeted_ad_units": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Resource names of the ad units that constitute the placement: " +
					"`networks/{network_code}/adUnits/{ad_unit_id}`. Reference managed ad units directly, " +
					"e.g. `[admanager_ad_unit.example.id]`. Modeled as a set: order is not significant and does " +
					"not cause drift. To target no ad units, omit the attribute; an explicit empty list is not " +
					"accepted (the API cannot distinguish it from absent).",
				Validators: []validator.Set{setvalidator.SizeAtLeast(1)},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The status of the placement: `ACTIVE`, `INACTIVE`, or `ARCHIVED`.",
			},
			"update_time": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The time the placement was last modified (RFC 3339).",
			},
			"skip_archive_on_destroy": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "When `true`, `terraform destroy` removes the placement from state without archiving it in " +
					"Ad Manager. Provider-side only; never sent to the API. Defaults to `false`.",
			},
		},
	}
}

func (r *placementResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// placementModelToAPI builds the API resource from the model, copying only the
// fields the API accepts on write. Output-only fields are never included.
func placementModelToAPI(ctx context.Context, m placementResourceModel) (*client.Placement, diag.Diagnostics) {
	var diags diag.Diagnostics
	p := &client.Placement{
		Name:        m.ID.ValueString(),
		DisplayName: m.DisplayName.ValueString(),
	}
	if isSet(m.Description) {
		p.Description = m.Description.ValueString()
	}
	if !m.TargetedAdUnits.IsNull() && !m.TargetedAdUnits.IsUnknown() {
		var units []string
		diags.Append(m.TargetedAdUnits.ElementsAs(ctx, &units, false)...)
		p.TargetedAdUnits = units
	}
	return p, diags
}

// placementAPIToModel maps an API resource into a full Terraform model,
// populating every attribute from what the API returned (honest drift).
// skipArchive is a provider-side-only value carried through unchanged.
func placementAPIToModel(ctx context.Context, p *client.Placement, skipArchive types.Bool) (placementResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	m := placementResourceModel{
		ID:                   types.StringValue(p.Name),
		PlacementID:          types.StringValue(placementNumericID(p)),
		DisplayName:          types.StringValue(p.DisplayName),
		Description:          stringOrNull(p.Description),
		PlacementCode:        stringOrNull(p.PlacementCode),
		Status:               stringOrNull(p.Status),
		UpdateTime:           stringOrNull(p.UpdateTime),
		SkipArchiveOnDestroy: skipArchive,
	}

	// targeted_ad_units is a plain optional: absent means "none targeted", so it
	// maps to null rather than an empty set.
	targeted, d := stringSliceToSet(ctx, p.TargetedAdUnits, false)
	diags.Append(d...)
	m.TargetedAdUnits = targeted

	return m, diags
}

// stringSliceToSet converts an API string slice into a Terraform set. When the
// slice is empty, an optional attribute maps to null and a computed attribute
// maps to an empty set (both known, both honest for "none"). It mirrors
// stringSliceToList for the set-typed attributes.
func stringSliceToSet(ctx context.Context, values []string, computed bool) (types.Set, diag.Diagnostics) {
	if len(values) == 0 {
		if computed {
			return types.SetValueMust(types.StringType, []attr.Value{}), nil
		}
		return types.SetNull(types.StringType), nil
	}
	return types.SetValueFrom(ctx, types.StringType, values)
}

// placementNumericID prefers the API-provided numeric id and falls back to
// parsing it out of the resource name.
func placementNumericID(p *client.Placement) string {
	if p.PlacementID != "" {
		return p.PlacementID
	}
	return numericIDFromName(p.Name)
}

// buildPlacementUpdateMask returns the API field names whose settable values
// differ between plan and state.
func buildPlacementUpdateMask(plan, state *placementResourceModel) []string {
	var mask []string
	add := func(changed bool, field string) {
		if changed {
			mask = append(mask, field)
		}
	}
	add(!plan.DisplayName.Equal(state.DisplayName), "displayName")
	add(!plan.Description.Equal(state.Description), "description")
	add(!plan.TargetedAdUnits.Equal(state.TargetedAdUnits), "targetedAdUnits")
	return mask
}

func (r *placementResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan placementResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	p, diags := placementModelToAPI(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	p.Name = "" // On create the resource name is unknown.

	created, err := r.client.CreatePlacement(ctx, p)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create placement", apiErrorDetail("creating placement", err))
		return
	}

	state, diags := placementAPIToModel(ctx, created, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *placementResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state placementResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	p, err := r.client.GetPlacement(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read placement", apiErrorDetail("reading placement "+state.ID.ValueString(), err))
		return
	}

	newState, diags := placementAPIToModel(ctx, p, state.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *placementResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state placementResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	mask := buildPlacementUpdateMask(&plan, &state)
	if len(mask) == 0 {
		// Nothing patchable changed (e.g. only skip_archive_on_destroy, which
		// never reaches the API). Computed attributes with no UseStateForUnknown
		// modifier (status, update_time) arrive Unknown in the plan; persisting
		// the plan would write unknown values into post-apply state, which
		// Terraform rejects. Carry the full prior state forward and apply only the
		// provider-side skip_archive_on_destroy.
		state.SkipArchiveOnDestroy = plan.SkipArchiveOnDestroy
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	p, diags := placementModelToAPI(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	p.Name = state.ID.ValueString() // The name is immutable; use the known one.

	updated, err := r.client.PatchPlacement(ctx, p, mask)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update placement", apiErrorDetail("updating placement "+p.Name, err))
		return
	}

	newState, diags := placementAPIToModel(ctx, updated, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *placementResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state placementResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.SkipArchiveOnDestroy.ValueBool() {
		return // Provider-side opt-out: forget the resource without touching Ad Manager.
	}

	name := state.ID.ValueString()
	err := r.client.ArchivePlacement(ctx, name)
	if err == nil {
		return
	}
	if client.IsNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Placement already absent",
			fmt.Sprintf("Placement %s was not found while archiving; it is treated as already destroyed.", name),
		)
		return
	}
	// The archive failed for some other reason. The batchArchive error alone
	// cannot tell "already archived" apart from a genuine block. Re-read the
	// placement and only tolerate the failure when it actually reads back
	// ARCHIVED (or is gone). Anything else is surfaced so the resource stays in
	// state instead of being silently dropped while still live in Ad Manager.
	if archived, verifyErr := r.placementIsArchived(ctx, name); verifyErr == nil && archived {
		resp.Diagnostics.AddWarning(
			"Placement already archived",
			fmt.Sprintf("Placement %s was already archived in Ad Manager; no action was taken.", name),
		)
		return
	}
	resp.Diagnostics.AddError("Unable to archive placement", apiErrorDetail("archiving placement "+name, err))
}

// placementIsArchived reports whether the placement currently reads back as
// ARCHIVED. A 404 counts as archived-or-gone. Any other read error is returned
// so the caller surfaces the original archive failure rather than dropping a
// live placement.
func (r *placementResource) placementIsArchived(ctx context.Context, name string) (bool, error) {
	p, err := r.client.GetPlacement(ctx, name)
	if err != nil {
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return p.Status == "ARCHIVED", nil
}

func (r *placementResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name := normalizePlacementName(strings.TrimSpace(req.ID), r.client.NetworkCode())
	if name == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be a full resource name (networks/{network_code}/placements/{placement_id}) or a bare numeric placement ID.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
}

// normalizePlacementName accepts either a full resource name or a bare numeric
// id and returns a full resource name scoped to networkCode.
func normalizePlacementName(id, networkCode string) string {
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "networks/") {
		return id
	}
	if !strings.Contains(id, "/") {
		return fmt.Sprintf("networks/%s/placements/%s", networkCode, id)
	}
	// A slash but not a full resource name (e.g. "placements/123" or a typo) is
	// malformed: return empty so ImportState emits the invalid-ID diagnostic.
	return ""
}
