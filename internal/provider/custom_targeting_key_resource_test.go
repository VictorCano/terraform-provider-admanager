package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

func TestCustomTargetingKeyResourceMetadata(t *testing.T) {
	r := NewCustomTargetingKeyResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "admanager"}, resp)
	if resp.TypeName != "admanager_custom_targeting_key" {
		t.Errorf("TypeName = %q, want admanager_custom_targeting_key", resp.TypeName)
	}
}

func TestCustomTargetingKeyResourceSchema(t *testing.T) {
	r := NewCustomTargetingKeyResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	attrs := resp.Schema.Attributes

	wantAttrs := []string{
		"id", "custom_targeting_key_id", "ad_tag_name", "display_name", "type",
		"reportable_type", "status", "skip_archive_on_destroy",
	}
	for _, name := range wantAttrs {
		if _, ok := attrs[name]; !ok {
			t.Errorf("schema missing attribute %q", name)
		}
	}
	for _, name := range []string{"ad_tag_name", "type", "reportable_type"} {
		if a := attrs[name]; !a.IsRequired() {
			t.Errorf("%s must be required", name)
		}
	}
	if a := attrs["display_name"]; !a.IsOptional() {
		t.Error("display_name must be optional")
	}
	for _, name := range []string{"id", "custom_targeting_key_id", "status"} {
		if a := attrs[name]; !a.IsComputed() {
			t.Errorf("%s must be computed", name)
		}
	}

	// ad_tag_name is Immutable in the API => RequiresReplace (a plan modifier).
	adTag, ok := attrs["ad_tag_name"].(rschema.StringAttribute)
	if !ok {
		t.Fatal("ad_tag_name should be a StringAttribute")
	}
	if len(adTag.PlanModifiers) == 0 {
		t.Error("ad_tag_name must carry a RequiresReplace plan modifier (it is immutable)")
	}
	// type is NOT marked Immutable in the discovery doc => it must be patchable,
	// so it carries no RequiresReplace modifier.
	keyType, ok := attrs["type"].(rschema.StringAttribute)
	if !ok {
		t.Fatal("type should be a StringAttribute")
	}
	if len(keyType.PlanModifiers) != 0 {
		t.Error("type must NOT force replacement: the discovery doc does not mark it Immutable")
	}
}

// apiCustomTargetingKey is a representative API response used by mapping tests.
func apiCustomTargetingKey() *client.CustomTargetingKey {
	return &client.CustomTargetingKey{
		Name:                 "networks/123456/customTargetingKeys/321",
		CustomTargetingKeyID: "321",
		AdTagName:            "genre",
		DisplayName:          "Genre",
		Type:                 "FREEFORM",
		ReportableType:       "ON",
		Status:               "ACTIVE",
	}
}

func TestCustomTargetingKeyAPIToModelMapsAllFields(t *testing.T) {
	m, diags := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(true))
	if diags.HasError() {
		t.Fatalf("customTargetingKeyAPIToModel: %v", diags)
	}
	if m.ID.ValueString() != "networks/123456/customTargetingKeys/321" {
		t.Errorf("id = %q", m.ID.ValueString())
	}
	if m.CustomTargetingKeyID.ValueString() != "321" {
		t.Errorf("custom_targeting_key_id = %q", m.CustomTargetingKeyID.ValueString())
	}
	if m.AdTagName.ValueString() != "genre" || m.DisplayName.ValueString() != "Genre" {
		t.Errorf("ad_tag_name/display_name = %q / %q", m.AdTagName.ValueString(), m.DisplayName.ValueString())
	}
	if m.Type.ValueString() != "FREEFORM" || m.ReportableType.ValueString() != "ON" {
		t.Errorf("type/reportable_type = %q / %q", m.Type.ValueString(), m.ReportableType.ValueString())
	}
	if m.Status.ValueString() != "ACTIVE" {
		t.Errorf("status = %q", m.Status.ValueString())
	}
	if !m.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy should be carried through unchanged")
	}
}

func TestCustomTargetingKeyAPIToModelNullsAbsentOptionals(t *testing.T) {
	k := &client.CustomTargetingKey{
		Name:                 "networks/123456/customTargetingKeys/322",
		CustomTargetingKeyID: "322",
		AdTagName:            "bare",
		Type:                 "PREDEFINED",
		ReportableType:       "OFF",
		Status:               "ACTIVE",
	}
	m, diags := customTargetingKeyAPIToModel(k, types.BoolNull())
	if diags.HasError() {
		t.Fatalf("customTargetingKeyAPIToModel: %v", diags)
	}
	if !m.DisplayName.IsNull() {
		t.Error("display_name should be null when the API omits it")
	}
}

func TestCustomTargetingKeyModelToAPISettableOnly(t *testing.T) {
	m, diags := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))
	if diags.HasError() {
		t.Fatalf("customTargetingKeyAPIToModel: %v", diags)
	}
	k := customTargetingKeyModelToAPI(m)
	if k.AdTagName != "genre" || k.DisplayName != "Genre" || k.Type != "FREEFORM" || k.ReportableType != "ON" {
		t.Errorf("settable fields not round-tripped: %+v", k)
	}
	// Output-only fields must NOT be carried into a write payload.
	if k.Status != "" || k.CustomTargetingKeyID != "" {
		t.Errorf("output-only fields leaked into write payload: %+v", k)
	}
}

func TestBuildCustomTargetingKeyUpdateMask(t *testing.T) {
	base, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolNull())

	t.Run("no changes", func(t *testing.T) {
		plan := base
		if mask := buildCustomTargetingKeyUpdateMask(&plan, &base); len(mask) != 0 {
			t.Errorf("mask = %v, want empty", mask)
		}
	})

	t.Run("display name change", func(t *testing.T) {
		plan := base
		plan.DisplayName = types.StringValue("Renamed")
		mask := buildCustomTargetingKeyUpdateMask(&plan, &base)
		if len(mask) != 1 || mask[0] != "displayName" {
			t.Errorf("mask = %v, want [displayName]", mask)
		}
	})

	t.Run("reportable type change uses API field name", func(t *testing.T) {
		plan := base
		plan.ReportableType = types.StringValue("CUSTOM_DIMENSION")
		mask := buildCustomTargetingKeyUpdateMask(&plan, &base)
		if len(mask) != 1 || mask[0] != "reportableType" {
			t.Errorf("mask = %v, want [reportableType]", mask)
		}
	})

	t.Run("ad_tag_name never enters the mask (immutable, forces replacement)", func(t *testing.T) {
		plan := base
		plan.AdTagName = types.StringValue("changed")
		mask := buildCustomTargetingKeyUpdateMask(&plan, &base)
		for _, f := range mask {
			if f == "adTagName" {
				t.Errorf("mask %v must not include the immutable adTagName", mask)
			}
		}
	})
}

func TestNormalizeCustomTargetingKeyName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"networks/123456/customTargetingKeys/321", "networks/123456/customTargetingKeys/321"},
		{"321", "networks/123456/customTargetingKeys/321"},
		{"", ""},
		{"customTargetingKeys/321", ""},
		{"network/123456/customTargetingKeys/321", ""},
	}
	for _, tc := range cases {
		if got := normalizeCustomTargetingKeyName(tc.in, "123456"); got != tc.want {
			t.Errorf("normalizeCustomTargetingKeyName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func customTargetingKeyTestSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewCustomTargetingKeyResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func newCustomTargetingKeyState(t *testing.T, m customTargetingKeyResourceModel) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: customTargetingKeyTestSchema(t)}
	if d := st.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building state: %v", d)
	}
	return st
}

// TestCustomTargetingKeyReadRemovesResourceWhenInactive: an out-of-band
// deactivation flips a key to INACTIVE. Unlike an ad unit / value (whose archived
// status is a computed-only field absorbed into state), a deactivated key can no
// longer be patched (the API 400s CUSTOM_TARGETING_ERROR_KEY_NOT_FOUND on any
// patch) and deactivation also resets reportable_type (ON -> OFF). Absorbing that
// drift would leave a resource that fails every subsequent apply. Instead the
// resource Read treats an INACTIVE key as gone and removes it from state, so the
// next plan is a clean recreate (create reactivates the same key — verified live).
func TestCustomTargetingKeyReadRemovesResourceWhenInactive(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"name":"networks/123456/customTargetingKeys/321","adTagName":"genre","type":"FREEFORM","reportableType":"OFF","status":"INACTIVE"}`))
	}))
	defer srv.Close()

	prior, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.ReadResponse{State: newCustomTargetingKeyState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newCustomTargetingKeyState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("an INACTIVE refresh must not error; diags = %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state must be removed (null) when the custom targeting key reads back INACTIVE")
	}
	// The removal carries an actionable warning naming the key. On an IMPORT of an
	// INACTIVE key the framework additionally emits its generic "Cannot import
	// non-existent remote object" error; this warning supplies the reason (the key
	// is deactivated and must be recreated), so the operator is not left guessing.
	if len(resp.Diagnostics.Warnings()) == 0 {
		t.Fatal("expected a warning explaining why the INACTIVE key was removed from state")
	}
	if w := resp.Diagnostics.Warnings()[0]; !strings.Contains(w.Detail(), "networks/123456/customTargetingKeys/321") ||
		!strings.Contains(strings.ToUpper(w.Detail()), "INACTIVE") {
		t.Errorf("warning detail should name the key and its INACTIVE status; got summary=%q detail=%q", w.Summary(), w.Detail())
	}
}

// TestCustomTargetingKeyReadKeepsActive is the complementary guard: an ACTIVE key
// is refreshed into state normally (the INACTIVE-removes branch must not fire for
// a live key).
func TestCustomTargetingKeyReadKeepsActive(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"networks/123456/customTargetingKeys/321","adTagName":"genre","displayName":"Genre","type":"FREEFORM","reportableType":"ON","status":"ACTIVE"}`))
	}))
	defer srv.Close()

	prior, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.ReadResponse{State: newCustomTargetingKeyState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newCustomTargetingKeyState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read of an ACTIVE key must not error; diags = %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("state must be retained for an ACTIVE key")
	}
	var got customTargetingKeyResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading result state: %v", d)
	}
	if got.Status.ValueString() != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE", got.Status.ValueString())
	}
}

// TestCustomTargetingKeyDeleteSurfacesRealDeactivateFailure: when
// batchDeactivate fails for a genuine reason and the key reads back ACTIVE,
// Delete must surface an error rather than silently drop a live key from state.
func TestCustomTargetingKeyDeleteSurfacesRealDeactivateFailure(t *testing.T) {
	ctx := context.Background()
	var deactivateCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchDeactivate"):
			deactivateCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Key cannot be deactivated"}}`))
		case r.Method == http.MethodGet:
			getCalls++
			_, _ = w.Write([]byte(`{"name":"networks/123456/customTargetingKeys/321","status":"ACTIVE"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	m, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newCustomTargetingKeyState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newCustomTargetingKeyState(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected an error when deactivate fails and the key is still ACTIVE; diags = %v", resp.Diagnostics)
	}
	if deactivateCalls != 1 || getCalls != 1 {
		t.Errorf("deactivateCalls=%d getCalls=%d, want 1 and 1 (re-verify via GET before tolerating)", deactivateCalls, getCalls)
	}
}

// TestCustomTargetingKeyDeleteToleratesAlreadyInactive confirms a deactivate
// failure is tolerated as success only when the key actually reads back INACTIVE.
func TestCustomTargetingKeyDeleteToleratesAlreadyInactive(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchDeactivate"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Key is already inactive"}}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"name":"networks/123456/customTargetingKeys/321","status":"INACTIVE"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	m, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newCustomTargetingKeyState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newCustomTargetingKeyState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("already-inactive key should be tolerated, got error: %v", resp.Diagnostics)
	}
	if len(resp.Diagnostics.Warnings()) == 0 {
		t.Error("expected a warning noting the key was already inactive")
	}
}

// TestCustomTargetingKeyDeleteToleratesAlreadyAbsent drives the DIRECT 404
// tolerance branch: batchDeactivate itself returns 404. Delete must treat the
// key as already destroyed without a re-read, mirroring the ad_unit reference
// (TestDeleteToleratesAlreadyAbsent). Pinning getCalls == 0 makes a regression
// that removes the direct client.IsNotFound(err) branch detectable: without it
// the 404 would fall through to customTargetingKeyIsInactive and issue a GET.
func TestCustomTargetingKeyDeleteToleratesAlreadyAbsent(t *testing.T) {
	ctx := context.Background()
	var deactivateCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchDeactivate"):
			deactivateCalls++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"CustomTargetingKey not found"}}`))
		case r.Method == http.MethodGet:
			getCalls++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"CustomTargetingKey not found"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	m, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newCustomTargetingKeyState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newCustomTargetingKeyState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("already-absent key should be tolerated: %v", resp.Diagnostics)
	}
	if deactivateCalls != 1 {
		t.Errorf("deactivateCalls = %d, want 1", deactivateCalls)
	}
	if getCalls != 0 {
		t.Errorf("getCalls = %d, want 0 (a 404 on deactivate is tolerated directly, without a re-read)", getCalls)
	}
}

func TestCustomTargetingKeyDeleteSkipTouchesNothing(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("skip_archive_on_destroy must not call the API; got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	m, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(true))
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.DeleteResponse{State: newCustomTargetingKeyState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newCustomTargetingKeyState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("skip delete should not error: %v", resp.Diagnostics)
	}
}

// TestCustomTargetingKeyUpdateEmptyMaskCarriesComputedFromState guards the no-op
// update branch (only skip_archive_on_destroy changed): the computed status,
// which arrives Unknown in the plan, must be carried forward from prior state.
func TestCustomTargetingKeyUpdateEmptyMaskCarriesComputedFromState(t *testing.T) {
	ctx := context.Background()
	state, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))

	plan := state
	plan.SkipArchiveOnDestroy = types.BoolValue(true)
	plan.Status = types.StringUnknown()

	sch := customTargetingKeyTestSchema(t)
	planObj := tfsdk.Plan{Schema: sch}
	if d := planObj.Set(ctx, &plan); d.HasError() {
		t.Fatalf("set plan: %v", d)
	}
	stateObj := tfsdk.State{Schema: sch}
	if d := stateObj.Set(ctx, &state); d.HasError() {
		t.Fatalf("set state: %v", d)
	}

	r := &customTargetingKeyResource{} // empty-mask branch must not call the API
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	r.Update(ctx, resource.UpdateRequest{Plan: planObj, State: stateObj}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	var got customTargetingKeyResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading result state: %v", d)
	}
	if !got.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy should be updated to true")
	}
	if got.Status.IsUnknown() {
		t.Error("computed status left unknown in post-apply state")
	}
	if got.Status.ValueString() != "ACTIVE" {
		t.Errorf("computed status not carried from state: %q", got.Status.ValueString())
	}
}
