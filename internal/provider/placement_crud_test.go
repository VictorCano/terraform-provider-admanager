package provider

// P0-2: in-process Create / Read / mask-Update happy paths for the placement
// resource. These success paths were acceptance-only until now (only Delete and
// the empty-mask Update branch had unit tests). Every fake serves realistic GAM
// placement JSON (shapes copied from apiPlacement / the client tests).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func newPlacementPlan(t *testing.T, m placementResourceModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: placementTestSchema(t)}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building plan: %v", d)
	}
	return p
}

// createdPlacementJSON is the API echo of a freshly created placement, with the
// server-assigned name, id, code, status, and update time populated.
const createdPlacementJSON = `{
	"name": "networks/123456/placements/789",
	"placementId": "789",
	"displayName": "Homepage Bundle",
	"description": "All homepage inventory",
	"targetedAdUnits": ["networks/123456/adUnits/456", "networks/123456/adUnits/457"],
	"placementCode": "abc123",
	"status": "ACTIVE",
	"updateTime": "2026-07-05T12:00:00Z"
}`

func TestPlacementCreatePersistsState(t *testing.T) {
	ctx := context.Background()
	var postCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/networks/123456/placements" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		postCalls++
		_, _ = w.Write([]byte(createdPlacementJSON))
	}))
	defer srv.Close()

	r := &placementResource{client: newAdUnitTestClient(t, srv)}
	plan := newPlacementPlan(t, placementResourceModel{
		DisplayName: types.StringValue("Homepage Bundle"),
		Description: types.StringValue("All homepage inventory"),
		TargetedAdUnits: types.SetValueMust(types.StringType, []attr.Value{
			types.StringValue("networks/123456/adUnits/456"),
			types.StringValue("networks/123456/adUnits/457"),
		}),
		SkipArchiveOnDestroy: types.BoolValue(false),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: placementTestSchema(t)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	if postCalls != 1 {
		t.Errorf("postCalls = %d, want 1", postCalls)
	}
	var got placementResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.ID.ValueString() != "networks/123456/placements/789" || got.PlacementID.ValueString() != "789" {
		t.Errorf("id/placement_id = %q / %q", got.ID.ValueString(), got.PlacementID.ValueString())
	}
	if got.PlacementCode.ValueString() != "abc123" || got.Status.ValueString() != "ACTIVE" {
		t.Errorf("computed fields not populated from create response: %+v", got)
	}
	var targeted []string
	got.TargetedAdUnits.ElementsAs(ctx, &targeted, false)
	if len(targeted) != 2 {
		t.Errorf("targeted_ad_units = %v, want 2 members", targeted)
	}
}

func TestPlacementReadRefreshesState(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/networks/123456/placements/789" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		// The live object drifted: display name and status differ from prior state.
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/placements/789",
			"placementId": "789",
			"displayName": "Renamed Bundle",
			"description": "All homepage inventory",
			"targetedAdUnits": ["networks/123456/adUnits/456", "networks/123456/adUnits/457"],
			"placementCode": "abc123",
			"status": "INACTIVE",
			"updateTime": "2026-07-06T09:00:00Z"
		}`))
	}))
	defer srv.Close()

	prior, _ := placementAPIToModel(ctx, apiPlacement(), types.BoolValue(true))
	r := &placementResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.ReadResponse{State: newPlacementState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newPlacementState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", resp.Diagnostics)
	}
	var got placementResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.DisplayName.ValueString() != "Renamed Bundle" || got.Status.ValueString() != "INACTIVE" {
		t.Errorf("read did not refresh drifted fields: %+v", got)
	}
	// skip_archive_on_destroy is provider-side only and must be carried through
	// the refresh unchanged, never read back from the API.
	if !got.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy must be carried through the read unchanged")
	}
}

// TestPlacementUpdateWithMaskAppliesPatch drives a display-name-only change and
// asserts the PATCH carries an exact updateMask and a body limited to the
// changed field: description and targeted_ad_units are null in both plan and
// state, so placementModelToAPI omits them and only displayName is sent.
func TestPlacementUpdateWithMaskAppliesPatch(t *testing.T) {
	ctx := context.Background()
	var gotMask string
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/networks/123456/placements/789" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotMask = r.URL.Query().Get("updateMask")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshaling PATCH body: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/placements/789",
			"placementId": "789",
			"displayName": "Renamed Bundle",
			"placementCode": "abc123",
			"status": "ACTIVE",
			"updateTime": "2026-07-06T09:00:00Z"
		}`))
	}))
	defer srv.Close()

	state := placementResourceModel{
		ID:                   types.StringValue("networks/123456/placements/789"),
		PlacementID:          types.StringValue("789"),
		DisplayName:          types.StringValue("Homepage Bundle"),
		Description:          types.StringNull(),
		PlacementCode:        types.StringValue("abc123"),
		TargetedAdUnits:      types.SetNull(types.StringType),
		Status:               types.StringValue("ACTIVE"),
		UpdateTime:           types.StringValue("2026-07-05T12:00:00Z"),
		SkipArchiveOnDestroy: types.BoolValue(false),
	}
	plan := state
	plan.DisplayName = types.StringValue("Renamed Bundle")
	// Plain-computed attributes arrive Unknown in the plan.
	plan.Status = types.StringUnknown()
	plan.UpdateTime = types.StringUnknown()

	r := &placementResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: placementTestSchema(t)}}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  newPlacementPlan(t, plan),
		State: newPlacementState(t, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", resp.Diagnostics)
	}
	if gotMask != "displayName" {
		t.Errorf("updateMask = %q, want exactly displayName", gotMask)
	}
	if _, ok := gotBody["displayName"]; !ok {
		t.Errorf("PATCH body missing displayName: %v", gotBody)
	}
	for _, unwanted := range []string{"description", "targetedAdUnits"} {
		if _, ok := gotBody[unwanted]; ok {
			t.Errorf("PATCH body should not carry unchanged/absent %q: %v", unwanted, gotBody)
		}
	}
	var got placementResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.DisplayName.ValueString() != "Renamed Bundle" {
		t.Errorf("display_name = %q, want Renamed Bundle", got.DisplayName.ValueString())
	}
}
