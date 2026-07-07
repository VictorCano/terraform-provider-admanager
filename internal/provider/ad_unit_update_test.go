package provider

// P0-3 (resource half): the ad_unit mask-driven Update path (PatchAdUnit) was
// acceptance-only. This drives it in-process, asserting the exact updateMask and
// request body, and that reconcileOmittedAppliedFields is wired on the update
// read-back: a PATCH response that omits appliedTargetWindow but echoes a
// corroborating effectiveTargetWindow keeps the planned target_window rather
// than collapsing it to null (the issue #1 old-network shape, on Update).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestAdUnitUpdateWithMaskAppliesPatch(t *testing.T) {
	ctx := context.Background()
	var gotMask string
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/networks/123456/adUnits/456" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotMask = r.URL.Query().Get("updateMask")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshaling PATCH body: %v", err)
		}
		// Old-network read-back: appliedTargetWindow is OMITTED, but the effective
		// twin echoes BLANK and corroborates the planned value.
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/adUnits/456",
			"adUnitId": "456",
			"displayName": "Renamed Leaderboard",
			"parentAdUnit": "networks/123456/adUnits/1",
			"adUnitCode": "homepage_leaderboard",
			"effectiveTargetWindow": "BLANK",
			"status": "ACTIVE",
			"updateTime": "2026-07-06T09:00:00Z"
		}`))
	}))
	defer srv.Close()

	// State and plan differ only in display_name; target_window stays BLANK.
	state, _ := adUnitAPIToModel(ctx, apiAdUnit(), types.BoolValue(false))
	plan := state
	plan.DisplayName = types.StringValue("Renamed Leaderboard")

	r := &adUnitResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: adUnitTestSchema(t)}}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  newAdUnitPlan(t, plan),
		State: newAdUnitState(t, state),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", resp.Diagnostics)
	}
	if gotMask != "displayName" {
		t.Errorf("updateMask = %q, want exactly displayName", gotMask)
	}
	if raw, ok := gotBody["displayName"]; !ok || string(raw) != `"Renamed Leaderboard"` {
		t.Errorf("PATCH body displayName = %s (present=%v), want \"Renamed Leaderboard\"", gotBody["displayName"], ok)
	}
	// Output-only fields must never be sent in a write payload.
	for _, unwanted := range []string{"status", "updateTime", "effectiveTargetWindow", "hasChildren"} {
		if _, ok := gotBody[unwanted]; ok {
			t.Errorf("PATCH body leaked output-only field %q: %v", unwanted, gotBody)
		}
	}

	var got adUnitResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.DisplayName.ValueString() != "Renamed Leaderboard" {
		t.Errorf("display_name = %q, want Renamed Leaderboard", got.DisplayName.ValueString())
	}
	// reconcileOmittedAppliedFields wiring: the omitted appliedTargetWindow is
	// corroborated by effectiveTargetWindow == BLANK == planned, so target_window
	// must stay BLANK rather than drift to null.
	if got.TargetWindow.IsNull() || got.TargetWindow.ValueString() != "BLANK" {
		t.Errorf("target_window = %v, want preserved BLANK (reconcile must run on the update read-back)", got.TargetWindow)
	}
}
