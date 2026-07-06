package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

func TestPlacementResourceMetadata(t *testing.T) {
	r := NewPlacementResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "admanager"}, resp)
	if resp.TypeName != "admanager_placement" {
		t.Errorf("TypeName = %q, want admanager_placement", resp.TypeName)
	}
}

func TestPlacementResourceSchema(t *testing.T) {
	r := NewPlacementResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	attrs := resp.Schema.Attributes

	wantAttrs := []string{
		"id", "placement_id", "display_name", "description", "placement_code",
		"targeted_ad_units", "status", "update_time", "skip_archive_on_destroy",
	}
	for _, name := range wantAttrs {
		if _, ok := attrs[name]; !ok {
			t.Errorf("schema missing attribute %q", name)
		}
	}
	if a := attrs["display_name"]; !a.IsRequired() {
		t.Error("display_name must be required")
	}
	for _, name := range []string{"id", "placement_id", "placement_code", "status", "update_time"} {
		if a := attrs[name]; !a.IsComputed() {
			t.Errorf("%s must be computed", name)
		}
	}
	for _, name := range []string{"description", "targeted_ad_units", "skip_archive_on_destroy"} {
		if a := attrs[name]; !a.IsOptional() {
			t.Errorf("%s must be optional", name)
		}
	}
	// placement_code is Google-assigned; it must never be settable.
	if a := attrs["placement_code"]; a.IsOptional() || a.IsRequired() {
		t.Error("placement_code must be computed-only (not settable)")
	}
}

// TestPlacementTargetedAdUnitsRejectsEmptySet guards edge 2 from the adversarial
// review: an explicit empty set (`targeted_ad_units = []`) is indistinguishable
// from absent at the API (omitempty drops it, the read-back is null), so writing
// it would trip Terraform's "inconsistent result after apply". A validator must
// reject it up front with a clear message; the canonical "none" form is omitting
// the attribute (null), which stays accepted.
func TestPlacementTargetedAdUnitsRejectsEmptySet(t *testing.T) {
	ctx := context.Background()
	attrs := placementTestSchema(t).Attributes
	setAttr, ok := attrs["targeted_ad_units"].(rschema.SetAttribute)
	if !ok {
		t.Fatalf("targeted_ad_units should be a SetAttribute, got %T", attrs["targeted_ad_units"])
	}
	if len(setAttr.Validators) == 0 {
		t.Fatal("targeted_ad_units must carry a validator rejecting an explicit empty set")
	}

	hasErr := func(v types.Set) bool {
		var resp validator.SetResponse
		req := validator.SetRequest{Path: path.Root("targeted_ad_units"), ConfigValue: v}
		for _, val := range setAttr.Validators {
			val.ValidateSet(ctx, req, &resp)
		}
		return resp.Diagnostics.HasError()
	}

	empty := types.SetValueMust(types.StringType, []attr.Value{})
	if !hasErr(empty) {
		t.Error("an explicit empty targeted_ad_units set must be rejected")
	}
	one := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("networks/123456/adUnits/456")})
	if hasErr(one) {
		t.Error("a one-element targeted_ad_units set must be accepted")
	}
	if hasErr(types.SetNull(types.StringType)) {
		t.Error("null targeted_ad_units (the canonical \"none\" form) must be accepted")
	}
}

// apiPlacement is a representative API response used by the mapping tests.
func apiPlacement() *client.Placement {
	return &client.Placement{
		Name:            "networks/123456/placements/789",
		PlacementID:     "789",
		DisplayName:     "Homepage Bundle",
		Description:     "All homepage inventory",
		TargetedAdUnits: []string{"networks/123456/adUnits/456", "networks/123456/adUnits/457"},
		PlacementCode:   "abc123",
		Status:          "ACTIVE",
		UpdateTime:      "2026-07-05T12:00:00Z",
	}
}

func TestPlacementAPIToModelMapsAllFields(t *testing.T) {
	ctx := context.Background()
	m, diags := placementAPIToModel(ctx, apiPlacement(), types.BoolValue(true))
	if diags.HasError() {
		t.Fatalf("placementAPIToModel: %v", diags)
	}
	if m.ID.ValueString() != "networks/123456/placements/789" {
		t.Errorf("id = %q", m.ID.ValueString())
	}
	if m.PlacementID.ValueString() != "789" {
		t.Errorf("placement_id = %q", m.PlacementID.ValueString())
	}
	if m.DisplayName.ValueString() != "Homepage Bundle" {
		t.Errorf("display_name = %q", m.DisplayName.ValueString())
	}
	if m.Description.ValueString() != "All homepage inventory" {
		t.Errorf("description = %q", m.Description.ValueString())
	}
	if m.PlacementCode.ValueString() != "abc123" {
		t.Errorf("placement_code = %q", m.PlacementCode.ValueString())
	}
	if m.Status.ValueString() != "ACTIVE" || m.UpdateTime.ValueString() != "2026-07-05T12:00:00Z" {
		t.Errorf("status/update_time = %q / %q", m.Status.ValueString(), m.UpdateTime.ValueString())
	}
	var targeted []string
	m.TargetedAdUnits.ElementsAs(ctx, &targeted, false)
	if len(targeted) != 2 || targeted[0] != "networks/123456/adUnits/456" {
		t.Errorf("targeted_ad_units mapping wrong: %v", targeted)
	}
	if !m.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy should be carried through unchanged")
	}
}

func TestPlacementAPIToModelNullsAbsentOptionals(t *testing.T) {
	p := &client.Placement{
		Name:          "networks/123456/placements/790",
		PlacementID:   "790",
		DisplayName:   "Bare",
		PlacementCode: "code790",
		Status:        "ACTIVE",
		UpdateTime:    "2026-07-05T12:00:00Z",
	}
	m, diags := placementAPIToModel(context.Background(), p, types.BoolNull())
	if diags.HasError() {
		t.Fatalf("placementAPIToModel: %v", diags)
	}
	if !m.Description.IsNull() {
		t.Error("description should be null when the API omits it")
	}
	if !m.TargetedAdUnits.IsNull() {
		t.Error("targeted_ad_units should be null when absent (optional)")
	}
}

func TestPlacementModelToAPISettableOnly(t *testing.T) {
	m, diags := placementAPIToModel(context.Background(), apiPlacement(), types.BoolValue(false))
	if diags.HasError() {
		t.Fatalf("placementAPIToModel: %v", diags)
	}
	p, diags := placementModelToAPI(context.Background(), m)
	if diags.HasError() {
		t.Fatalf("placementModelToAPI: %v", diags)
	}
	if p.DisplayName != "Homepage Bundle" || p.Description != "All homepage inventory" {
		t.Errorf("settable fields not round-tripped: %+v", p)
	}
	if len(p.TargetedAdUnits) != 2 || p.TargetedAdUnits[0] != "networks/123456/adUnits/456" {
		t.Errorf("targeted_ad_units not round-tripped: %v", p.TargetedAdUnits)
	}
	// Output-only fields must NOT be carried into a write payload.
	if p.PlacementCode != "" || p.Status != "" || p.UpdateTime != "" || p.PlacementID != "" {
		t.Errorf("output-only fields leaked into write payload: %+v", p)
	}
}

func TestBuildPlacementUpdateMask(t *testing.T) {
	base, _ := placementAPIToModel(context.Background(), apiPlacement(), types.BoolNull())

	t.Run("no changes", func(t *testing.T) {
		plan := base
		if mask := buildPlacementUpdateMask(&plan, &base); len(mask) != 0 {
			t.Errorf("mask = %v, want empty", mask)
		}
	})

	t.Run("display name change", func(t *testing.T) {
		plan := base
		plan.DisplayName = types.StringValue("Renamed")
		mask := buildPlacementUpdateMask(&plan, &base)
		if len(mask) != 1 || mask[0] != "displayName" {
			t.Errorf("mask = %v, want [displayName]", mask)
		}
	})

	t.Run("targeted ad units change uses API field name", func(t *testing.T) {
		plan := base
		plan.TargetedAdUnits = types.SetValueMust(types.StringType, []attr.Value{
			types.StringValue("networks/123456/adUnits/456"),
		})
		mask := buildPlacementUpdateMask(&plan, &base)
		if len(mask) != 1 || mask[0] != "targetedAdUnits" {
			t.Errorf("mask = %v, want [targetedAdUnits]", mask)
		}
	})

	t.Run("description change", func(t *testing.T) {
		plan := base
		plan.Description = types.StringNull()
		mask := buildPlacementUpdateMask(&plan, &base)
		if len(mask) != 1 || mask[0] != "description" {
			t.Errorf("mask = %v, want [description]", mask)
		}
	})

	// targeted_ad_units is a set: membership, not order, defines equality. A
	// reordering returned by the API (same members, different order) must NOT
	// produce a spurious update. This guards against the order-sensitive
	// List.Equal drift the adversarial review flagged.
	t.Run("reordered targeted ad units are equal (no spurious update)", func(t *testing.T) {
		plan := base
		plan.TargetedAdUnits = types.SetValueMust(types.StringType, []attr.Value{
			types.StringValue("networks/123456/adUnits/457"),
			types.StringValue("networks/123456/adUnits/456"),
		})
		if mask := buildPlacementUpdateMask(&plan, &base); len(mask) != 0 {
			t.Errorf("mask = %v, want empty (a reorder of the same members is not a change)", mask)
		}
	})
}

func TestNormalizePlacementName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"networks/123456/placements/789", "networks/123456/placements/789"},
		{"789", "networks/123456/placements/789"},
		{"", ""},
		{"placements/789", ""},
		{"network/123456/placements/789", ""},
	}
	for _, tc := range cases {
		if got := normalizePlacementName(tc.in, "123456"); got != tc.want {
			t.Errorf("normalizePlacementName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// placementTestSchema returns the resource schema for building tfsdk states/plans.
func placementTestSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewPlacementResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func newPlacementState(t *testing.T, m placementResourceModel) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: placementTestSchema(t)}
	if d := st.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building state: %v", d)
	}
	return st
}

// TestPlacementDeleteSurfacesRealArchiveFailure: when batchArchive fails for a
// genuine reason and the placement reads back ACTIVE, Delete must surface an
// error rather than silently drop a live placement from state.
func TestPlacementDeleteSurfacesRealArchiveFailure(t *testing.T) {
	ctx := context.Background()
	var archiveCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchArchive"):
			archiveCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Placement cannot be archived"}}`))
		case r.Method == http.MethodGet:
			getCalls++
			_, _ = w.Write([]byte(`{"name":"networks/123456/placements/789","status":"ACTIVE"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	m, _ := placementAPIToModel(ctx, apiPlacement(), types.BoolValue(false))
	r := &placementResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newPlacementState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newPlacementState(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected an error when archive fails and the placement is still ACTIVE; diags = %v", resp.Diagnostics)
	}
	if archiveCalls != 1 || getCalls != 1 {
		t.Errorf("archiveCalls=%d getCalls=%d, want 1 and 1 (re-verify via GET before tolerating)", archiveCalls, getCalls)
	}
}

func TestPlacementDeleteToleratesAlreadyArchived(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchArchive"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Placement is already archived"}}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"name":"networks/123456/placements/789","status":"ARCHIVED"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	m, _ := placementAPIToModel(ctx, apiPlacement(), types.BoolValue(false))
	r := &placementResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newPlacementState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newPlacementState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("already-archived placement should be tolerated, got error: %v", resp.Diagnostics)
	}
	if len(resp.Diagnostics.Warnings()) == 0 {
		t.Error("expected a warning noting the placement was already archived")
	}
}

// TestPlacementDeleteToleratesAlreadyAbsent drives the DIRECT 404 tolerance
// branch: batchArchive itself returns 404. Delete must treat the placement as
// already destroyed without a re-read, mirroring the ad_unit reference
// (TestDeleteToleratesAlreadyAbsent). Pinning getCalls == 0 makes a regression
// that removes the direct client.IsNotFound(err) branch detectable: without it
// the 404 would fall through to placementIsArchived and issue a GET re-read.
func TestPlacementDeleteToleratesAlreadyAbsent(t *testing.T) {
	ctx := context.Background()
	var archiveCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchArchive"):
			archiveCalls++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"Placement not found"}}`))
		case r.Method == http.MethodGet:
			getCalls++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"Placement not found"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	m, _ := placementAPIToModel(ctx, apiPlacement(), types.BoolValue(false))
	r := &placementResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newPlacementState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newPlacementState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("already-absent placement should be tolerated: %v", resp.Diagnostics)
	}
	if archiveCalls != 1 {
		t.Errorf("archiveCalls = %d, want 1", archiveCalls)
	}
	if getCalls != 0 {
		t.Errorf("getCalls = %d, want 0 (a 404 on archive is tolerated directly, without a re-read)", getCalls)
	}
}

func TestPlacementDeleteSkipArchiveTouchesNothing(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("skip_archive_on_destroy must not call the API; got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	m, _ := placementAPIToModel(ctx, apiPlacement(), types.BoolValue(true))
	r := &placementResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newPlacementState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newPlacementState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("skip-archive delete should not error: %v", resp.Diagnostics)
	}
}

// TestPlacementUpdateEmptyMaskCarriesComputedFromState guards the no-op update
// branch (only skip_archive_on_destroy changed): computed attributes that arrive
// Unknown in the plan must be carried forward from prior state, not persisted
// unknown.
func TestPlacementUpdateEmptyMaskCarriesComputedFromState(t *testing.T) {
	ctx := context.Background()
	state, _ := placementAPIToModel(ctx, apiPlacement(), types.BoolValue(false))

	plan := state
	plan.SkipArchiveOnDestroy = types.BoolValue(true)
	plan.Status = types.StringUnknown()
	plan.UpdateTime = types.StringUnknown()

	sch := placementTestSchema(t)
	planObj := tfsdk.Plan{Schema: sch}
	if d := planObj.Set(ctx, &plan); d.HasError() {
		t.Fatalf("set plan: %v", d)
	}
	stateObj := tfsdk.State{Schema: sch}
	if d := stateObj.Set(ctx, &state); d.HasError() {
		t.Fatalf("set state: %v", d)
	}

	r := &placementResource{} // empty-mask branch must not call the API
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	r.Update(ctx, resource.UpdateRequest{Plan: planObj, State: stateObj}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	var got placementResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading result state: %v", d)
	}
	if !got.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy should be updated to true")
	}
	if got.Status.IsUnknown() || got.UpdateTime.IsUnknown() {
		t.Error("computed attributes left unknown in post-apply state")
	}
	if got.Status.ValueString() != "ACTIVE" || got.UpdateTime.ValueString() != "2026-07-05T12:00:00Z" {
		t.Errorf("computed values not carried from state: status=%q update_time=%q",
			got.Status.ValueString(), got.UpdateTime.ValueString())
	}
}
