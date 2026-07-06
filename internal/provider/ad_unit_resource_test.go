package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/oauth2"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

func boolPtr(b bool) *bool { return &b }

func TestAdUnitResourceMetadata(t *testing.T) {
	r := NewAdUnitResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "admanager"}, resp)
	if resp.TypeName != "admanager_ad_unit" {
		t.Errorf("TypeName = %q, want admanager_ad_unit", resp.TypeName)
	}
}

func TestAdUnitResourceSchema(t *testing.T) {
	r := NewAdUnitResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	attrs := resp.Schema.Attributes

	wantAttrs := []string{
		"id", "ad_unit_id", "parent_ad_unit", "display_name", "ad_unit_code",
		"description", "target_window", "effective_target_window",
		"explicitly_targeted", "applied_adsense_enabled", "effective_adsense_enabled",
		"smart_size_mode", "refresh_delay", "external_set_top_box_channel_id",
		"status", "has_children", "update_time", "sizes", "applied_teams", "teams",
		"skip_archive_on_destroy",
	}
	for _, name := range wantAttrs {
		if _, ok := attrs[name]; !ok {
			t.Errorf("schema missing attribute %q", name)
		}
	}

	// Deferred label / frequency-cap attributes must NOT be present as
	// half-working stubs.
	for _, name := range []string{
		"applied_labels", "effective_applied_labels",
		"applied_label_frequency_caps", "effective_label_frequency_caps",
	} {
		if _, ok := attrs[name]; ok {
			t.Errorf("deferred attribute %q should not be in the schema yet", name)
		}
	}

	// Required.
	if a := attrs["parent_ad_unit"]; !a.IsRequired() {
		t.Error("parent_ad_unit must be required")
	}
	if a := attrs["display_name"]; !a.IsRequired() {
		t.Error("display_name must be required")
	}
	// Computed (output-only).
	for _, name := range []string{
		"id", "ad_unit_id", "effective_target_window", "effective_adsense_enabled",
		"status", "has_children", "update_time", "teams",
	} {
		if a := attrs[name]; !a.IsComputed() {
			t.Errorf("%s must be computed", name)
		}
	}
	// Optional-and-computed pairs.
	for _, name := range []string{"ad_unit_code", "explicitly_targeted", "smart_size_mode"} {
		a := attrs[name]
		if !a.IsOptional() || !a.IsComputed() {
			t.Errorf("%s must be optional and computed", name)
		}
	}
}

func TestNumericIDFromName(t *testing.T) {
	cases := map[string]string{
		"networks/123456/adUnits/456": "456",
		"456":                         "456",
		"":                            "",
	}
	for in, want := range cases {
		if got := numericIDFromName(in); got != want {
			t.Errorf("numericIDFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

// apiAdUnit is a representative API response used by the mapping tests.
func apiAdUnit() *client.AdUnit {
	return &client.AdUnit{
		Name:                    "networks/123456/adUnits/456",
		AdUnitID:                "456",
		DisplayName:             "Homepage Leaderboard",
		ParentAdUnit:            "networks/123456/adUnits/1",
		AdUnitCode:              "homepage_leaderboard",
		Description:             "Top of the homepage",
		AppliedTargetWindow:     "BLANK",
		EffectiveTargetWindow:   "BLANK",
		ExplicitlyTargeted:      boolPtr(true),
		AppliedAdsenseEnabled:   boolPtr(false),
		EffectiveAdsenseEnabled: false,
		SmartSizeMode:           "NONE",
		RefreshDelay:            "30s",
		Status:                  "ACTIVE",
		HasChildren:             false,
		UpdateTime:              "2026-07-05T12:00:00Z",
		AdUnitSizes: []client.AdUnitSize{
			{Size: client.Size{Width: 300, Height: 250, SizeType: "PIXEL"}, EnvironmentType: "BROWSER"},
			{
				Size:            client.Size{Width: 640, Height: 480, SizeType: "PIXEL"},
				EnvironmentType: "VIDEO_PLAYER",
				Companions:      []client.Size{{Width: 300, Height: 250, SizeType: "PIXEL"}},
			},
		},
		AppliedTeams: []string{"networks/123456/teams/7"},
		Teams:        []string{"networks/123456/teams/7", "networks/123456/teams/9"},
	}
}

func TestAdUnitAPIToModelMapsAllFields(t *testing.T) {
	ctx := context.Background()
	m, diags := adUnitAPIToModel(ctx, apiAdUnit(), types.BoolValue(true))
	if diags.HasError() {
		t.Fatalf("adUnitAPIToModel: %v", diags)
	}
	if m.ID.ValueString() != "networks/123456/adUnits/456" {
		t.Errorf("id = %q", m.ID.ValueString())
	}
	if m.AdUnitID.ValueString() != "456" {
		t.Errorf("ad_unit_id = %q", m.AdUnitID.ValueString())
	}
	if m.DisplayName.ValueString() != "Homepage Leaderboard" {
		t.Errorf("display_name = %q", m.DisplayName.ValueString())
	}
	if m.TargetWindow.ValueString() != "BLANK" || m.EffectiveTargetWindow.ValueString() != "BLANK" {
		t.Errorf("target windows = %q / %q", m.TargetWindow.ValueString(), m.EffectiveTargetWindow.ValueString())
	}
	if !m.ExplicitlyTargeted.ValueBool() {
		t.Error("explicitly_targeted should be true")
	}
	if m.AppliedAdsenseEnabled.IsNull() || m.AppliedAdsenseEnabled.ValueBool() {
		t.Errorf("applied_adsense_enabled = %v, want known false", m.AppliedAdsenseEnabled)
	}
	if m.Status.ValueString() != "ACTIVE" || m.UpdateTime.ValueString() != "2026-07-05T12:00:00Z" {
		t.Errorf("status/update_time = %q / %q", m.Status.ValueString(), m.UpdateTime.ValueString())
	}
	if len(m.Sizes) != 2 {
		t.Fatalf("sizes len = %d, want 2", len(m.Sizes))
	}
	if m.Sizes[0].Width.ValueInt64() != 300 || m.Sizes[0].Height.ValueInt64() != 250 {
		t.Errorf("size[0] = %dx%d", m.Sizes[0].Width.ValueInt64(), m.Sizes[0].Height.ValueInt64())
	}
	if len(m.Sizes[1].Companions) != 1 || m.Sizes[1].Companions[0].Width.ValueInt64() != 300 {
		t.Errorf("companion mapping wrong: %+v", m.Sizes[1].Companions)
	}
	if !m.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy should be carried through unchanged")
	}
	// Applied vs effective teams must be distinct lists.
	var applied, all []string
	m.AppliedTeams.ElementsAs(ctx, &applied, false)
	m.Teams.ElementsAs(ctx, &all, false)
	if len(applied) != 1 || len(all) != 2 {
		t.Errorf("teams mapping wrong: applied=%v teams=%v", applied, all)
	}
}

func TestAdUnitAPIToModelNullsAbsentOptionals(t *testing.T) {
	// A freshly created root-child ad unit with only the required fields set:
	// absent optionals must map to null, not to zero values that would fake a
	// clean plan.
	au := &client.AdUnit{
		Name:                  "networks/123456/adUnits/789",
		AdUnitID:              "789",
		DisplayName:           "Bare",
		ParentAdUnit:          "networks/123456/adUnits/1",
		AdUnitCode:            "bare",
		EffectiveTargetWindow: "TOP",
		Status:                "ACTIVE",
		UpdateTime:            "2026-07-05T12:00:00Z",
	}
	m, diags := adUnitAPIToModel(context.Background(), au, types.BoolNull())
	if diags.HasError() {
		t.Fatalf("adUnitAPIToModel: %v", diags)
	}
	if !m.Description.IsNull() {
		t.Error("description should be null when the API omits it")
	}
	if !m.TargetWindow.IsNull() {
		t.Error("target_window should be null when appliedTargetWindow is absent")
	}
	if !m.AppliedAdsenseEnabled.IsNull() {
		t.Error("applied_adsense_enabled should be null when absent (inherited)")
	}
	if !m.RefreshDelay.IsNull() {
		t.Error("refresh_delay should be null when absent")
	}
	if len(m.Sizes) != 0 {
		t.Errorf("sizes should be empty when absent, got %+v", m.Sizes)
	}
	if !m.AppliedTeams.IsNull() {
		t.Error("applied_teams should be null when absent")
	}
	// explicitly_targeted is optional+computed: absent means the effective
	// default (false), and it must be a known value, never null.
	if m.ExplicitlyTargeted.IsNull() || m.ExplicitlyTargeted.ValueBool() {
		t.Errorf("explicitly_targeted = %v, want known false", m.ExplicitlyTargeted)
	}
}

func TestAdUnitModelToAPISettableOnly(t *testing.T) {
	m, diags := adUnitAPIToModel(context.Background(), apiAdUnit(), types.BoolValue(false))
	if diags.HasError() {
		t.Fatalf("adUnitAPIToModel: %v", diags)
	}
	au, diags := adUnitModelToAPI(context.Background(), m)
	if diags.HasError() {
		t.Fatalf("adUnitModelToAPI: %v", diags)
	}
	// Settable fields round-trip.
	if au.DisplayName != "Homepage Leaderboard" || au.ParentAdUnit != "networks/123456/adUnits/1" ||
		au.AdUnitCode != "homepage_leaderboard" || au.Description != "Top of the homepage" ||
		au.AppliedTargetWindow != "BLANK" || au.SmartSizeMode != "NONE" || au.RefreshDelay != "30s" {
		t.Errorf("settable fields not round-tripped: %+v", au)
	}
	if au.ExplicitlyTargeted == nil || !*au.ExplicitlyTargeted {
		t.Error("explicitly_targeted not round-tripped")
	}
	if au.AppliedAdsenseEnabled == nil || *au.AppliedAdsenseEnabled {
		t.Error("applied_adsense_enabled=false not round-tripped (must send an explicit false)")
	}
	if len(au.AdUnitSizes) != 2 || au.AdUnitSizes[0].Size.Width != 300 ||
		len(au.AdUnitSizes[1].Companions) != 1 {
		t.Errorf("sizes not round-tripped: %+v", au.AdUnitSizes)
	}
	if len(au.AppliedTeams) != 1 || au.AppliedTeams[0] != "networks/123456/teams/7" {
		t.Errorf("applied_teams not round-tripped: %v", au.AppliedTeams)
	}
	// Output-only fields must NOT be carried into a write payload.
	if au.EffectiveTargetWindow != "" || au.Status != "" || au.UpdateTime != "" ||
		au.HasChildren || len(au.Teams) != 0 || au.EffectiveAdsenseEnabled {
		t.Errorf("output-only fields leaked into write payload: %+v", au)
	}
}

func TestBuildAdUnitUpdateMask(t *testing.T) {
	base, _ := adUnitAPIToModel(context.Background(), apiAdUnit(), types.BoolNull())

	t.Run("no changes", func(t *testing.T) {
		plan := base
		if mask := buildAdUnitUpdateMask(&plan, &base); len(mask) != 0 {
			t.Errorf("mask = %v, want empty", mask)
		}
	})

	t.Run("display name change", func(t *testing.T) {
		plan := base
		plan.DisplayName = types.StringValue("Renamed")
		mask := buildAdUnitUpdateMask(&plan, &base)
		if len(mask) != 1 || mask[0] != "displayName" {
			t.Errorf("mask = %v, want [displayName]", mask)
		}
	})

	t.Run("multiple changes use API field names", func(t *testing.T) {
		plan := base
		plan.Description = types.StringValue("New desc")
		plan.TargetWindow = types.StringValue("TOP")
		plan.RefreshDelay = types.StringNull()
		plan.ExternalSetTopBoxChannelID = types.StringValue("chan-1")
		plan.Sizes = []sizeModel{{
			Width: types.Int64Value(728), Height: types.Int64Value(90),
			SizeType: types.StringValue("PIXEL"), EnvironmentType: types.StringValue("BROWSER"),
		}}
		mask := buildAdUnitUpdateMask(&plan, &base)
		got := map[string]bool{}
		for _, f := range mask {
			got[f] = true
		}
		for _, want := range []string{"description", "appliedTargetWindow", "refreshDelay", "externalSetTopBoxChannelId", "adUnitSizes"} {
			if !got[want] {
				t.Errorf("mask %v missing %q", mask, want)
			}
		}
		if got["displayName"] {
			t.Errorf("mask %v should not include unchanged displayName", mask)
		}
	})

	t.Run("applied teams change", func(t *testing.T) {
		plan := base
		plan.AppliedTeams = types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("networks/123456/teams/7"),
			types.StringValue("networks/123456/teams/42"),
		})
		mask := buildAdUnitUpdateMask(&plan, &base)
		if len(mask) != 1 || mask[0] != "appliedTeams" {
			t.Errorf("mask = %v, want [appliedTeams]", mask)
		}
	})
}

func TestNormalizeAdUnitName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"networks/123456/adUnits/456", "networks/123456/adUnits/456"}, // full resource name passes through
		{"456", "networks/123456/adUnits/456"},                         // bare numeric id is expanded
		{"", ""},                                                       // empty -> invalid
		{"adUnits/123", ""},                                            // slash but not a full resource name -> invalid
		{"network/123456/adUnits/456", ""},                             // singular "network" typo -> invalid
	}
	for _, tc := range cases {
		if got := normalizeAdUnitName(tc.in, "123456"); got != tc.want {
			t.Errorf("normalizeAdUnitName(%q, %q) = %q, want %q", tc.in, "123456", got, tc.want)
		}
	}
}

// adUnitTestSchema returns the resource schema for building tfsdk states/plans
// in unit tests that drive Update/Delete directly.
func adUnitTestSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewAdUnitResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// newAdUnitState builds a tfsdk.State populated from m.
func newAdUnitState(t *testing.T, m adUnitResourceModel) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: adUnitTestSchema(t)}
	if d := st.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building state: %v", d)
	}
	return st
}

// newAdUnitPlan builds a tfsdk.Plan populated from m.
func newAdUnitPlan(t *testing.T, m adUnitResourceModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: adUnitTestSchema(t)}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building plan: %v", d)
	}
	return p
}

// newAdUnitTestClient builds a client pointed at srv with a static fake token.
func newAdUnitTestClient(t *testing.T, srv *httptest.Server) *client.Client {
	t.Helper()
	c, err := client.New(context.Background(), client.Config{
		NetworkCode: "123456",
		BaseURL:     srv.URL,
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}),
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c
}

// TestDeleteSurfacesRealArchiveFailure is the regression guard for the
// over-broad already-archived tolerance: when batchArchive fails for a genuine
// reason (FAILED_PRECONDITION: still-active children) and the ad unit reads
// back as ACTIVE, Delete must surface an error so the resource stays in state
// rather than being silently dropped while it is still live in Ad Manager.
func TestDeleteSurfacesRealArchiveFailure(t *testing.T) {
	ctx := context.Background()
	var archiveCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchArchive"):
			archiveCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Ad unit cannot be archived because it has active children"}}`))
		case r.Method == http.MethodGet:
			getCalls++
			_, _ = w.Write([]byte(`{"name":"networks/123456/adUnits/456","status":"ACTIVE"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	m, _ := adUnitAPIToModel(ctx, apiAdUnit(), types.BoolValue(false))
	r := &adUnitResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newAdUnitState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newAdUnitState(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected an error when archive fails and the unit is still ACTIVE; diags = %v", resp.Diagnostics)
	}
	if archiveCalls != 1 || getCalls != 1 {
		t.Errorf("archiveCalls=%d getCalls=%d, want 1 and 1 (re-verify via GET before tolerating)", archiveCalls, getCalls)
	}
}

// TestDeleteToleratesAlreadyArchived confirms that an archive failure is
// tolerated as success only when the unit actually reads back ARCHIVED.
func TestDeleteToleratesAlreadyArchived(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchArchive"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Ad unit is already archived"}}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"name":"networks/123456/adUnits/456","status":"ARCHIVED"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	m, _ := adUnitAPIToModel(ctx, apiAdUnit(), types.BoolValue(false))
	r := &adUnitResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newAdUnitState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newAdUnitState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("already-archived unit should be tolerated, got error: %v", resp.Diagnostics)
	}
	if len(resp.Diagnostics.Warnings()) == 0 {
		t.Error("expected a warning noting the unit was already archived")
	}
}

// TestDeleteToleratesAlreadyAbsent confirms a 404 on archive is treated as
// already destroyed.
func TestDeleteToleratesAlreadyAbsent(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"AdUnit not found"}}`))
	}))
	defer srv.Close()

	m, _ := adUnitAPIToModel(ctx, apiAdUnit(), types.BoolValue(false))
	r := &adUnitResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newAdUnitState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newAdUnitState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("already-absent unit should be tolerated: %v", resp.Diagnostics)
	}
}

// TestDeleteSkipArchiveTouchesNothing confirms skip_archive_on_destroy makes
// Delete a state-only operation that never calls the API.
func TestDeleteSkipArchiveTouchesNothing(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("skip_archive_on_destroy must not call the API; got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	m, _ := adUnitAPIToModel(ctx, apiAdUnit(), types.BoolValue(true))
	r := &adUnitResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newAdUnitState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newAdUnitState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("skip-archive delete should not error: %v", resp.Diagnostics)
	}
}

// TestUpdateEmptyMaskCarriesComputedFromState guards the no-op update branch
// (only skip_archive_on_destroy changed): the framework marks null-config
// computed attributes Unknown in the plan, and persisting that plan directly
// would write unknown values into post-apply state, which Terraform rejects.
// The branch must carry the known computed values forward from prior state.
func TestUpdateEmptyMaskCarriesComputedFromState(t *testing.T) {
	ctx := context.Background()
	state, _ := adUnitAPIToModel(ctx, apiAdUnit(), types.BoolValue(false))

	plan := state
	plan.SkipArchiveOnDestroy = types.BoolValue(true)
	// Reproduce how the framework presents the plan: computed attributes with no
	// UseStateForUnknown modifier arrive Unknown.
	plan.EffectiveTargetWindow = types.StringUnknown()
	plan.EffectiveAdsenseEnabled = types.BoolUnknown()
	plan.Status = types.StringUnknown()
	plan.HasChildren = types.BoolUnknown()
	plan.UpdateTime = types.StringUnknown()
	plan.Teams = types.ListUnknown(types.StringType)

	sch := adUnitTestSchema(t)
	planObj := tfsdk.Plan{Schema: sch}
	if d := planObj.Set(ctx, &plan); d.HasError() {
		t.Fatalf("set plan: %v", d)
	}
	stateObj := tfsdk.State{Schema: sch}
	if d := stateObj.Set(ctx, &state); d.HasError() {
		t.Fatalf("set state: %v", d)
	}

	r := &adUnitResource{} // empty-mask branch must not call the API
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	r.Update(ctx, resource.UpdateRequest{Plan: planObj, State: stateObj}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	var got adUnitResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading result state: %v", d)
	}
	if !got.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy should be updated to true")
	}
	unknowns := map[string]bool{
		"effective_target_window":   got.EffectiveTargetWindow.IsUnknown(),
		"effective_adsense_enabled": got.EffectiveAdsenseEnabled.IsUnknown(),
		"status":                    got.Status.IsUnknown(),
		"has_children":              got.HasChildren.IsUnknown(),
		"update_time":               got.UpdateTime.IsUnknown(),
		"teams":                     got.Teams.IsUnknown(),
	}
	for name, unknown := range unknowns {
		if unknown {
			t.Errorf("computed attribute %q left unknown in post-apply state", name)
		}
	}
	if got.Status.ValueString() != "ACTIVE" || got.UpdateTime.ValueString() != "2026-07-05T12:00:00Z" {
		t.Errorf("computed values not carried from state: status=%q update_time=%q",
			got.Status.ValueString(), got.UpdateTime.ValueString())
	}
}
