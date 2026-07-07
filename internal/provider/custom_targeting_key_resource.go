package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// Enum value sets sourced from the discovery document (rev 20260701). The
// UNSPECIFIED sentinels are intentionally excluded: they are never valid input.
var (
	customTargetingKeyTypeValues       = []string{"PREDEFINED", "FREEFORM"}
	customTargetingKeyReportableValues = []string{"OFF", "ON", "CUSTOM_DIMENSION"}
)

// adTagNameMaxLength is the API's documented limit on adTagName (10 characters).
const adTagNameMaxLength = 10

// customTargetingKeyStatusInactive is the API status of a deactivated key. Keys
// have no ARCHIVED state; INACTIVE is the lifecycle end state (see the client
// CustomTargetingKey.Status doc).
const customTargetingKeyStatusInactive = "INACTIVE"

// customTargetingKeyAdTagNameRegex enforces the documented character denylist for
// a key's adTagName: it may not contain " ' = ! + # * ~ ; ^ ( ) < > [ ] or any
// whitespace. Rejecting these at plan time turns a deferred server-side error into
// an immediate one. The denylist is a superset of the value's (keys additionally
// forbid whitespace).
var customTargetingKeyAdTagNameRegex = regexp.MustCompile(`\A[^"'=!+#*~;^()<>\[\]\s]*\z`)

// adTagNameForbiddenCharsMessage is shared by the key and value denylist
// validators; the whitespace clause is appended by the key resource only.
const adTagNameForbiddenCharsMessage = `must not contain any of: " ' = ! + # * ~ ; ^ ( ) < > [ ]`

// Interface assertions.
var (
	_ resource.Resource                = (*customTargetingKeyResource)(nil)
	_ resource.ResourceWithConfigure   = (*customTargetingKeyResource)(nil)
	_ resource.ResourceWithImportState = (*customTargetingKeyResource)(nil)
)

// NewCustomTargetingKeyResource is the factory registered with the provider.
func NewCustomTargetingKeyResource() resource.Resource {
	return &customTargetingKeyResource{}
}

type customTargetingKeyResource struct {
	client *client.Client
}

// customTargetingKeyResourceModel is the Terraform view of a custom targeting
// key. The API field "type" is exposed under the attribute name "type".
type customTargetingKeyResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	CustomTargetingKeyID types.String `tfsdk:"custom_targeting_key_id"`
	AdTagName            types.String `tfsdk:"ad_tag_name"`
	DisplayName          types.String `tfsdk:"display_name"`
	Type                 types.String `tfsdk:"type"`
	ReportableType       types.String `tfsdk:"reportable_type"`
	Status               types.String `tfsdk:"status"`
	SkipArchiveOnDestroy types.Bool   `tfsdk:"skip_archive_on_destroy"`
}

func (r *customTargetingKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_targeting_key"
}

func (r *customTargetingKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Google Ad Manager [custom targeting key](https://developers.google.com/ad-manager/api/beta/reference/rest/v1/networks.customTargetingKeys). " +
			"Custom targeting keys, together with their values, define key-value targeting criteria for line items.\n\n" +
			"~> **Destroy deactivates, it does not delete.** The Ad Manager API has no hard delete or archive for " +
			"custom targeting keys. `terraform destroy` **deactivates** the key via `customTargetingKeys:batchDeactivate` " +
			"(its status becomes `INACTIVE`). Set `skip_archive_on_destroy = true` to remove the key from Terraform " +
			"state without touching Ad Manager.\n\n" +
			"~> **Deactivating a key outside Terraform triggers a recreate.** A deactivated (`INACTIVE`) key can no " +
			"longer be patched by Ad Manager, and deactivation also resets `reportable_type` (`ON` -> `OFF`). If a key " +
			"managed here is deactivated out of band, the next `terraform plan` treats it as gone and plans to recreate " +
			"it. The recreate is non-destructive: creating a key that reuses the same `ad_tag_name` reactivates the " +
			"existing `INACTIVE` key (reusing its ID) and applies the configured `display_name`, `type`, and " +
			"`reportable_type`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The full resource name of the key: `networks/{network_code}/customTargetingKeys/{custom_targeting_key_id}`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"custom_targeting_key_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The numeric custom targeting key ID, parsed from the resource name for convenience.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ad_tag_name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The name of the key as used in ad tags. **Immutable**: changing it forces replacement. " +
					"Maximum 10 characters; may not contain `\"`, `'`, `=`, `!`, `+`, `#`, `*`, `~`, `;`, `^`, `(`, `)`, " +
					"`<`, `>`, `[`, `]`, or whitespace.",
				Validators: []validator.String{
					stringvalidator.LengthAtMost(adTagNameMaxLength),
					stringvalidator.RegexMatches(customTargetingKeyAdTagNameRegex, adTagNameForbiddenCharsMessage+" or whitespace"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"display_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "A descriptive name for the key.",
			},
			"type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Whether line items select from predefined values or create new ones: " +
					"`PREDEFINED` (a fixed set of values) or `FREEFORM`.",
				Validators: []validator.String{stringvalidator.OneOf(customTargetingKeyTypeValues...)},
			},
			"reportable_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The reportable state of the key: `OFF` (not available for reporting), `ON` " +
					"(available in the query tool), or `CUSTOM_DIMENSION` (available as a custom dimension).",
				Validators: []validator.String{stringvalidator.OneOf(customTargetingKeyReportableValues...)},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The status of the key: `ACTIVE` or `INACTIVE`. Custom targeting keys have no archived state.",
			},
			"skip_archive_on_destroy": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "When `true`, `terraform destroy` removes the key from state without deactivating it in " +
					"Ad Manager. Provider-side only; never sent to the API. Defaults to `false`. Note: unlike ad units and " +
					"placements (which archive on destroy), custom targeting keys are **deactivated**; this flag opts out of " +
					"that deactivation. The attribute keeps the same name across resources for consistency.",
			},
		},
	}
}

func (r *customTargetingKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// customTargetingKeyModelToAPI builds the API resource from the model, copying
// only the fields the API accepts on write. Output-only fields are never
// included. adTagName is settable on create; it is immutable thereafter (the
// schema forces replacement) so it is never sent in a patch mask.
func customTargetingKeyModelToAPI(m customTargetingKeyResourceModel) *client.CustomTargetingKey {
	k := &client.CustomTargetingKey{
		Name:           m.ID.ValueString(),
		AdTagName:      m.AdTagName.ValueString(),
		Type:           m.Type.ValueString(),
		ReportableType: m.ReportableType.ValueString(),
	}
	if isSet(m.DisplayName) {
		k.DisplayName = m.DisplayName.ValueString()
	}
	return k
}

// customTargetingKeyAPIToModel maps an API resource into a full Terraform model,
// populating every attribute from what the API returned (honest drift).
// skipArchive is a provider-side-only value carried through unchanged.
func customTargetingKeyAPIToModel(k *client.CustomTargetingKey, skipArchive types.Bool) (customTargetingKeyResourceModel, diag.Diagnostics) {
	m := customTargetingKeyResourceModel{
		ID:                   types.StringValue(k.Name),
		CustomTargetingKeyID: types.StringValue(customTargetingKeyNumericID(k)),
		AdTagName:            stringOrNull(k.AdTagName),
		DisplayName:          stringOrNull(k.DisplayName),
		Type:                 stringOrNull(k.Type),
		ReportableType:       stringOrNull(k.ReportableType),
		Status:               stringOrNull(k.Status),
		SkipArchiveOnDestroy: skipArchive,
	}
	return m, nil
}

// customTargetingKeyNumericID prefers the API-provided numeric id and falls back
// to parsing it out of the resource name.
func customTargetingKeyNumericID(k *client.CustomTargetingKey) string {
	if k.CustomTargetingKeyID != "" {
		return k.CustomTargetingKeyID
	}
	return numericIDFromName(k.Name)
}

// buildCustomTargetingKeyUpdateMask returns the API field names whose settable
// values differ between plan and state. adTagName is immutable (it forces
// replacement) and is never included.
func buildCustomTargetingKeyUpdateMask(plan, state *customTargetingKeyResourceModel) []string {
	var mask []string
	add := func(changed bool, field string) {
		if changed {
			mask = append(mask, field)
		}
	}
	add(!plan.DisplayName.Equal(state.DisplayName), "displayName")
	add(!plan.Type.Equal(state.Type), "type")
	add(!plan.ReportableType.Equal(state.ReportableType), "reportableType")
	return mask
}

func (r *customTargetingKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customTargetingKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	k := customTargetingKeyModelToAPI(plan)
	k.Name = "" // On create the resource name is unknown.

	created, err := r.client.CreateCustomTargetingKey(ctx, k)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create custom targeting key", apiErrorDetail("creating custom targeting key", err))
		return
	}

	state, diags := customTargetingKeyAPIToModel(created, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *customTargetingKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customTargetingKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	k, err := r.client.GetCustomTargetingKey(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read custom targeting key", apiErrorDetail("reading custom targeting key "+state.ID.ValueString(), err))
		return
	}

	// A deactivated key is treated as gone. The API has no reactivate-in-place for
	// a managed key: patching an INACTIVE key fails with
	// CUSTOM_TARGETING_ERROR_KEY_NOT_FOUND (live-probed 2026-07-07), and
	// deactivation also resets reportableType (ON -> OFF), so absorbing the drift
	// would leave a resource that can never apply again. Removing it from state
	// makes the next plan a clean recreate; because a create reusing the same
	// ad_tag_name reactivates the existing INACTIVE key (also live-probed), the
	// recreate is non-destructive.
	if k.Status == customTargetingKeyStatusInactive {
		resp.Diagnostics.AddWarning(
			"Custom targeting key is inactive",
			fmt.Sprintf("Custom targeting key %s is INACTIVE in Ad Manager and has been removed from Terraform state; "+
				"a subsequent plan will recreate it (a create reusing the same ad_tag_name reactivates the existing key). "+
				"If this happened during import, the key you referenced is deactivated: reactivate it in Ad Manager before "+
				"importing, or let Terraform recreate it.", k.Name),
		)
		resp.State.RemoveResource(ctx)
		return
	}

	newState, diags := customTargetingKeyAPIToModel(k, state.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *customTargetingKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state customTargetingKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	mask := buildCustomTargetingKeyUpdateMask(&plan, &state)
	if len(mask) == 0 {
		// Nothing patchable changed (e.g. only skip_archive_on_destroy, which
		// never reaches the API). The computed status arrives Unknown in the plan;
		// persisting the plan would write an unknown value into post-apply state,
		// which Terraform rejects. Carry the full prior state forward and apply
		// only the provider-side skip_archive_on_destroy.
		state.SkipArchiveOnDestroy = plan.SkipArchiveOnDestroy
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	k := customTargetingKeyModelToAPI(plan)
	k.Name = state.ID.ValueString() // The name is immutable; use the known one.

	updated, err := r.client.PatchCustomTargetingKey(ctx, k, mask)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update custom targeting key", apiErrorDetail("updating custom targeting key "+k.Name, err))
		return
	}

	newState, diags := customTargetingKeyAPIToModel(updated, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *customTargetingKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state customTargetingKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.SkipArchiveOnDestroy.ValueBool() {
		return // Provider-side opt-out: forget the resource without touching Ad Manager.
	}

	name := state.ID.ValueString()
	err := r.client.DeactivateCustomTargetingKey(ctx, name)
	if err == nil {
		return
	}
	if client.IsNotFound(err) {
		resp.Diagnostics.AddWarning(
			"Custom targeting key already absent",
			fmt.Sprintf("Custom targeting key %s was not found while deactivating; it is treated as already destroyed.", name),
		)
		return
	}
	// The deactivate failed for some other reason. The batch error alone cannot
	// tell "already inactive" apart from a genuine block. Re-read the key and
	// only tolerate the failure when it actually reads back INACTIVE (or is
	// gone). Anything else is surfaced so the resource stays in state instead of
	// being silently dropped while still active in Ad Manager.
	if inactive, verifyErr := r.customTargetingKeyIsInactive(ctx, name); verifyErr == nil && inactive {
		resp.Diagnostics.AddWarning(
			"Custom targeting key already inactive",
			fmt.Sprintf("Custom targeting key %s was already inactive in Ad Manager; no action was taken.", name),
		)
		return
	}
	resp.Diagnostics.AddError("Unable to deactivate custom targeting key", apiErrorDetail("deactivating custom targeting key "+name, err))
}

// customTargetingKeyIsInactive reports whether the key currently reads back as
// INACTIVE. A 404 counts as inactive-or-gone. Any other read error is returned
// so the caller surfaces the original deactivate failure rather than dropping a
// live key.
func (r *customTargetingKeyResource) customTargetingKeyIsInactive(ctx context.Context, name string) (bool, error) {
	k, err := r.client.GetCustomTargetingKey(ctx, name)
	if err != nil {
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return k.Status == customTargetingKeyStatusInactive, nil
}

func (r *customTargetingKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name := normalizeCustomTargetingKeyName(strings.TrimSpace(req.ID), r.client.NetworkCode())
	if name == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be a full resource name (networks/{network_code}/customTargetingKeys/{custom_targeting_key_id}) or a bare numeric custom targeting key ID.",
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
}

// normalizeCustomTargetingKeyName accepts either a full resource name or a bare
// numeric id and returns a full resource name scoped to networkCode.
func normalizeCustomTargetingKeyName(id, networkCode string) string {
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "networks/") {
		return id
	}
	if !strings.Contains(id, "/") {
		return fmt.Sprintf("networks/%s/customTargetingKeys/%s", networkCode, id)
	}
	// A slash but not a full resource name is malformed: return empty so
	// ImportState emits the invalid-ID diagnostic.
	return ""
}
