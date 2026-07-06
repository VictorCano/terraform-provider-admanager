package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
	"github.com/VictorCano/terraform-provider-admanager/internal/soap"
)

// customTargetingValueMatchTypes is the match_type enum sourced from the
// discovery document (rev 20260701). The UNSPECIFIED sentinel is excluded: it is
// never valid input.
var customTargetingValueMatchTypes = []string{"EXACT", "BROAD", "PREFIX", "BROAD_PREFIX", "SUFFIX", "CONTAINS"}

// adTagNameValueMaxLength is the API's documented limit on a value's adTagName
// (the SOAP "name"): 40 characters.
const adTagNameValueMaxLength = 40

// soapLayerDocLink points at the README section explaining the SOAP write path.
const soapLayerDocLink = "https://github.com/VictorCano/terraform-provider-admanager/blob/main/README.md#custom-targeting-values-use-a-soap-compatibility-layer"

// customTargetingKeyReferenceRegex constrains custom_targeting_key to the canonical
// key resource name (networks/{network_code}/customTargetingKeys/{id}, both
// segments numeric). This is exactly the form the REST read-back returns after a
// SOAP create, so a validated value round-trips: a bare numeric id — which
// soap.KeyIDFromResourceName tolerates internally — would canonicalize on read-back
// and trip "inconsistent result after apply" on this Required attribute (and force
// perpetual replacement on later plans). Rejecting it up front fails fast, before
// any value is created in Ad Manager, rather than orphaning a created value.
var customTargetingKeyReferenceRegex = regexp.MustCompile(`^networks/[0-9]+/customTargetingKeys/[0-9]+$`)

// Interface assertions.
var (
	_ resource.Resource                = (*customTargetingValueResource)(nil)
	_ resource.ResourceWithConfigure   = (*customTargetingValueResource)(nil)
	_ resource.ResourceWithImportState = (*customTargetingValueResource)(nil)
)

// NewCustomTargetingValueResource is the factory registered with the provider.
func NewCustomTargetingValueResource() resource.Resource {
	return &customTargetingValueResource{}
}

// customTargetingValueResource is a hybrid resource: it READS through the REST
// client and WRITES through the SOAP shim (the REST API has no value write
// endpoints). Both talk to Ad Manager through the same rate limiter and
// credentials; see internal/soap.
type customTargetingValueResource struct {
	client *client.Client
	soap   *soap.Client
}

// customTargetingValueResourceModel is the Terraform view of a custom targeting
// value.
type customTargetingValueResourceModel struct {
	ID                     types.String `tfsdk:"id"`
	CustomTargetingValueID types.String `tfsdk:"custom_targeting_value_id"`
	CustomTargetingKey     types.String `tfsdk:"custom_targeting_key"`
	AdTagName              types.String `tfsdk:"ad_tag_name"`
	DisplayName            types.String `tfsdk:"display_name"`
	MatchType              types.String `tfsdk:"match_type"`
	Status                 types.String `tfsdk:"status"`
	SkipArchiveOnDestroy   types.Bool   `tfsdk:"skip_archive_on_destroy"`
}

func (r *customTargetingValueResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_targeting_value"
}

func (r *customTargetingValueResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Google Ad Manager [custom targeting value](https://developers.google.com/ad-manager/api/beta/reference/rest/v1/networks.customTargetingValues) " +
			"under a custom targeting key. Values are the right-hand side of key-value targeting (e.g. key `car`, value `honda`).\n\n" +
			"~> **Writes use a SOAP compatibility layer.** Custom targeting values are read-only in the Ad Manager REST API, so this " +
			"provider reads them over REST but performs create, update, and destroy through the legacy SOAP `CustomTargetingService`. " +
			"This is an implementation detail — the Terraform interface is identical to every other resource and the SOAP layer will be " +
			"removed transparently once the REST API ships value write endpoints. See " +
			"[Custom targeting values use a SOAP compatibility layer](" + soapLayerDocLink + ").\n\n" +
			"~> **Destroy deactivates, it does not delete.** `terraform destroy` **deactivates** the value (its status becomes " +
			"`INACTIVE`); values have no hard delete. Set `skip_archive_on_destroy = true` to remove the value from Terraform state " +
			"without touching Ad Manager.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The full (flat) resource name of the value: `networks/{network_code}/customTargetingValues/{custom_targeting_value_id}`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"custom_targeting_value_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The numeric custom targeting value ID, parsed from the resource name for convenience.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"custom_targeting_key": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The full resource name of the parent [custom targeting key](https://developers.google.com/ad-manager/api/beta/reference/rest/v1/networks.customTargetingKeys), " +
					"e.g. `admanager_custom_targeting_key.example.id` (`networks/{network_code}/customTargetingKeys/{custom_targeting_key_id}`). " +
					"Must be the full resource name, not a bare numeric id. **Immutable**: changing it forces replacement.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						customTargetingKeyReferenceRegex,
						"must be a full custom targeting key resource name, e.g. networks/{network_code}/customTargetingKeys/{id} (typically admanager_custom_targeting_key.example.id), not a bare numeric id",
					),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"ad_tag_name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The value string as used in ad tags (for key `car`, a value like `honda`). **Immutable**: changing it forces " +
					"replacement. Maximum 40 characters; may not contain `\"`, `'`, `=`, `!`, `+`, `#`, `*`, `~`, `;`, `^`, `(`, `)`, `<`, `>`, `[`, or `]`.",
				Validators:    []validator.String{stringvalidator.LengthAtMost(adTagNameValueMaxLength)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"display_name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "A descriptive name for the value. This is the only mutable attribute.",
			},
			"match_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "How the value string is matched against ad requests: `EXACT`, `BROAD`, `PREFIX`, `BROAD_PREFIX`, `SUFFIX`, or " +
					"`CONTAINS`. **Immutable**: changing it forces replacement.",
				Validators:    []validator.String{stringvalidator.OneOf(customTargetingValueMatchTypes...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The status of the value: `ACTIVE` or `INACTIVE`. Values have no archived state; the destroy end state is `INACTIVE`.",
			},
			"skip_archive_on_destroy": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "When `true`, `terraform destroy` removes the value from state without deactivating it in Ad Manager. " +
					"Provider-side only; never sent to the API. Defaults to `false`. The attribute keeps the same name across resources for " +
					"consistency, even though values are **deactivated** (not archived) on destroy.",
			},
		},
	}
}

func (r *customTargetingValueResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
	// Build the SOAP shim from the REST client's shared infrastructure so value
	// writes reuse the same oauth2 HTTP client, token, rate limiter, and network
	// code — nothing here builds parallel auth or a second token bucket.
	r.soap = soap.NewClient(soap.Config{
		HTTPClient:      c.HTTPClient(),
		Limiter:         c.Limiter(),
		NetworkCode:     c.NetworkCode(),
		ApplicationName: c.UserAgent(),
	})
}

// customTargetingValueAPIToModel maps a REST read into a full Terraform model,
// populating every attribute from what the API returned (honest drift). The
// numeric custom_targeting_value_id is parsed from the resource name because the
// discovery schema has no dedicated field for it. skipArchive is a
// provider-side-only value carried through unchanged.
func customTargetingValueAPIToModel(v *client.CustomTargetingValue, skipArchive types.Bool) customTargetingValueResourceModel {
	return customTargetingValueResourceModel{
		ID:                     types.StringValue(v.Name),
		CustomTargetingValueID: stringOrNull(numericIDFromName(v.Name)),
		CustomTargetingKey:     stringOrNull(v.CustomTargetingKey),
		AdTagName:              stringOrNull(v.AdTagName),
		DisplayName:            stringOrNull(v.DisplayName),
		MatchType:              stringOrNull(v.MatchType),
		Status:                 stringOrNull(v.Status),
		SkipArchiveOnDestroy:   skipArchive,
	}
}

// soapValueToModel builds a model from a SOAP create/update response. It is the
// fallback used only when the canonical REST read cannot run (so state — the id
// especially — is never lost after a successful write). The SOAP object carries
// no key resource name, so customTargetingKey is taken from the plan/state value
// that produced the write; restName is the flat REST resource name derived from
// the numeric id.
func soapValueToModel(v *soap.Value, restName string, customTargetingKey types.String, skipArchive types.Bool) customTargetingValueResourceModel {
	valueID := ""
	if v.ID != 0 {
		valueID = strconv.FormatInt(v.ID, 10)
	}
	return customTargetingValueResourceModel{
		ID:                     types.StringValue(restName),
		CustomTargetingValueID: stringOrNull(valueID),
		CustomTargetingKey:     customTargetingKey,
		AdTagName:              stringOrNull(v.Name), // SOAP "name" is REST "adTagName".
		DisplayName:            stringOrNull(v.DisplayName),
		MatchType:              stringOrNull(v.MatchType),
		Status:                 stringOrNull(v.Status),
		SkipArchiveOnDestroy:   skipArchive,
	}
}

func (r *customTargetingValueResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customTargetingValueResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keyID, err := soap.KeyIDFromResourceName(plan.CustomTargetingKey.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid custom_targeting_key",
			fmt.Sprintf("custom_targeting_key %q is not a valid key resource name or numeric id: %v", plan.CustomTargetingKey.ValueString(), err),
		)
		return
	}

	v := soap.Value{
		CustomTargetingKeyID: keyID,
		Name:                 plan.AdTagName.ValueString(),
		MatchType:            plan.MatchType.ValueString(),
	}
	if isSet(plan.DisplayName) {
		v.DisplayName = plan.DisplayName.ValueString()
	}

	created, err := r.soap.CreateCustomTargetingValue(ctx, v)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create custom targeting value", soapErrorDetail("creating custom targeting value", err))
		return
	}

	// The value now exists in Ad Manager. Bridge the SOAP numeric id to the flat
	// REST resource name and read it back canonically (hybrid pattern: reads are
	// authoritative over REST). If that read fails, keep the SOAP-derived state —
	// including the id — so a created value is never orphaned by a lost state.
	restName := r.soap.ValueResourceName(created.ID)
	got, err := r.client.GetCustomTargetingValue(ctx, restName)
	if err != nil {
		fallback := soapValueToModel(created, restName, plan.CustomTargetingKey, plan.SkipArchiveOnDestroy)
		resp.Diagnostics.AddWarning(
			"Custom targeting value created but not read back",
			"The value was created in Ad Manager but the follow-up REST read failed: "+
				apiErrorDetail("reading custom targeting value "+restName, err)+
				". State was populated from the create response; run 'terraform apply' again to reconcile computed fields.",
		)
		resp.Diagnostics.Append(resp.State.Set(ctx, &fallback)...)
		return
	}

	state := customTargetingValueAPIToModel(got, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *customTargetingValueResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customTargetingValueResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	v, err := r.client.GetCustomTargetingValue(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read custom targeting value", apiErrorDetail("reading custom targeting value "+state.ID.ValueString(), err))
		return
	}

	newState := customTargetingValueAPIToModel(v, state.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *customTargetingValueResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state customTargetingValueResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// display_name is the only mutable attribute; custom_targeting_key,
	// ad_tag_name, and match_type all force replacement. If display_name did not
	// change, nothing reaches the API (e.g. only skip_archive_on_destroy changed).
	// The computed status arrives Unknown in the plan, so carry the full prior
	// state forward rather than writing an unknown into post-apply state.
	if plan.DisplayName.Equal(state.DisplayName) {
		state.SkipArchiveOnDestroy = plan.SkipArchiveOnDestroy
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	keyID, err := soap.KeyIDFromResourceName(state.CustomTargetingKey.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid custom_targeting_key", fmt.Sprintf("stored custom_targeting_key %q is unparseable: %v", state.CustomTargetingKey.ValueString(), err))
		return
	}
	valueID, err := soap.ValueIDFromResourceName(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid resource id", fmt.Sprintf("stored id %q is unparseable: %v", state.ID.ValueString(), err))
		return
	}

	// SOAP update replaces the whole object. Carry the immutable fields from state
	// and apply the new display_name (empty clears it). Status is read-only and is
	// deliberately left unset.
	v := soap.Value{
		CustomTargetingKeyID: keyID,
		ID:                   valueID,
		Name:                 state.AdTagName.ValueString(),
		MatchType:            state.MatchType.ValueString(),
	}
	if isSet(plan.DisplayName) {
		v.DisplayName = plan.DisplayName.ValueString()
	}

	if _, err := r.soap.UpdateCustomTargetingValue(ctx, v); err != nil {
		resp.Diagnostics.AddError("Unable to update custom targeting value", soapErrorDetail("updating custom targeting value "+state.ID.ValueString(), err))
		return
	}

	// Refresh canonically via REST. If the read-back fails, the update already
	// applied, so keep prior state with the new display_name instead of erroring.
	got, err := r.client.GetCustomTargetingValue(ctx, state.ID.ValueString())
	if err != nil {
		state.DisplayName = plan.DisplayName
		state.SkipArchiveOnDestroy = plan.SkipArchiveOnDestroy
		resp.Diagnostics.AddWarning(
			"Custom targeting value updated but not read back",
			"The value was updated in Ad Manager but the follow-up REST read failed: "+
				apiErrorDetail("reading custom targeting value "+state.ID.ValueString(), err)+
				". State reflects the applied change; run 'terraform apply' again to reconcile computed fields.",
		)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	newState := customTargetingValueAPIToModel(got, plan.SkipArchiveOnDestroy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *customTargetingValueResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state customTargetingValueResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.SkipArchiveOnDestroy.ValueBool() {
		return // Provider-side opt-out: forget the resource without touching Ad Manager.
	}

	keyID, err := soap.KeyIDFromResourceName(state.CustomTargetingKey.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid custom_targeting_key", fmt.Sprintf("stored custom_targeting_key %q is unparseable: %v", state.CustomTargetingKey.ValueString(), err))
		return
	}
	valueID, err := soap.ValueIDFromResourceName(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid resource id", fmt.Sprintf("stored id %q is unparseable: %v", state.ID.ValueString(), err))
		return
	}

	// Deactivate exactly this value. The SOAP filter is scoped by bind variables
	// to (customTargetingKeyId, id), so no unrelated value can be swept in.
	_, err = r.soap.DeleteCustomTargetingValue(ctx, keyID, valueID)
	if err == nil {
		return
	}

	// The action failed for some reason. A SOAP fault alone cannot tell "already
	// inactive/gone" apart from a genuine block, so re-read over REST and only
	// tolerate the failure when the value actually reads back INACTIVE (or is
	// gone). Anything else is surfaced so the value stays in state rather than
	// being silently dropped while still active in Ad Manager.
	if inactive, verifyErr := r.valueIsInactiveOrGone(ctx, state.ID.ValueString()); verifyErr == nil && inactive {
		resp.Diagnostics.AddWarning(
			"Custom targeting value already inactive",
			fmt.Sprintf("Custom targeting value %s was already inactive or gone in Ad Manager; no action was taken.", state.ID.ValueString()),
		)
		return
	}
	resp.Diagnostics.AddError("Unable to deactivate custom targeting value", soapErrorDetail("deactivating custom targeting value "+state.ID.ValueString(), err))
}

// valueIsInactiveOrGone reports whether the value currently reads back as
// INACTIVE. A 404 counts as inactive-or-gone. Any other read error is returned so
// the caller surfaces the original delete failure rather than dropping a live
// value.
func (r *customTargetingValueResource) valueIsInactiveOrGone(ctx context.Context, name string) (bool, error) {
	v, err := r.client.GetCustomTargetingValue(ctx, name)
	if err != nil {
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return v.Status == "INACTIVE", nil
}

func (r *customTargetingValueResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name := normalizeCustomTargetingValueName(strings.TrimSpace(req.ID), r.client.NetworkCode())
	if name == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be a full resource name (networks/{network_code}/customTargetingValues/{custom_targeting_value_id}) or a bare numeric custom targeting value ID.",
		)
		return
	}
	// Only the id is set here; the subsequent Read populates every other attribute
	// (including custom_targeting_key) from the REST API.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
}

// normalizeCustomTargetingValueName accepts either a full resource name or a bare
// numeric id and returns a full resource name scoped to networkCode.
func normalizeCustomTargetingValueName(id, networkCode string) string {
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "networks/") {
		return id
	}
	if !strings.Contains(id, "/") {
		return fmt.Sprintf("networks/%s/customTargetingValues/%s", networkCode, id)
	}
	// A slash but not a full resource name is malformed: return empty so
	// ImportState emits the invalid-ID diagnostic.
	return ""
}

// soapErrorDetail renders an actionable diagnostic detail for a SOAP write
// failure. It relies on *soap.SOAPError.Error(), which surfaces the fault codes
// and never includes the request envelope or the Authorization token.
func soapErrorDetail(operation string, err error) string {
	var se *soap.SOAPError
	if errors.As(err, &se) {
		return fmt.Sprintf("While %s: %s", operation, se.Error())
	}
	return fmt.Sprintf("While %s: %s", operation, err.Error())
}
