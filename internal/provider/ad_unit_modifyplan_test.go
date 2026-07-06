package provider

// Tests for issue #2 (plan-time warning): replacing an ad unit that pins an
// ad_unit_code archives the current unit on destroy, and the archived unit keeps
// its immutable ad_unit_code reserved, so the replacement create fails on the
// reserved code. ModifyPlan warns about this at plan time (no API calls).
//
// All tests build framework state/plan values; ModifyPlan makes no API calls.

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestAdUnitResourceImplementsModifyPlan is a compile-time-ish guard that the
// resource wires up the ResourceWithModifyPlan interface.
func TestAdUnitResourceImplementsModifyPlan(t *testing.T) {
	if _, ok := NewAdUnitResource().(resource.ResourceWithModifyPlan); !ok {
		t.Fatal("adUnitResource must implement resource.ResourceWithModifyPlan")
	}
}

func TestAdUnitReplaceReservesCode(t *testing.T) {
	base := func() adUnitResourceModel {
		return adUnitResourceModel{
			adUnitModel: adUnitModel{
				ID:           types.StringValue("networks/123456/adUnits/456"),
				ParentAdUnit: types.StringValue("networks/123456/adUnits/1"),
				AdUnitCode:   types.StringValue("homepage_leaderboard"),
			},
			SkipArchiveOnDestroy: types.BoolValue(false),
		}
	}

	t.Run("replace via parent change reserves code", func(t *testing.T) {
		state, plan := base(), base()
		plan.ParentAdUnit = types.StringValue("networks/123456/adUnits/2")
		if !adUnitReplaceReservesCode(state, plan, false) {
			t.Error("parent change with a code set must warn")
		}
	})

	t.Run("rename (code change) does not reserve — create sends the new unreserved code", func(t *testing.T) {
		// Changing ad_unit_code old->new is a RequiresReplace, but the archived
		// old unit reserves "homepage_leaderboard" while the replacement create
		// SENDS the new code "new_code" (unreserved) — no collision. The trap
		// only fires when the create reuses the SAME code the archived unit holds.
		state, plan := base(), base()
		plan.AdUnitCode = types.StringValue("new_code")
		if adUnitReplaceReservesCode(state, plan, false) {
			t.Error("a rename sends a fresh, unreserved code — the create does not collide, so no warning")
		}
	})

	t.Run("parent change plus rename does not reserve", func(t *testing.T) {
		// Even when another immutable also forces the replace, a changed
		// ad_unit_code means the create sends the new code — still no collision.
		state, plan := base(), base()
		plan.ParentAdUnit = types.StringValue("networks/123456/adUnits/2")
		plan.AdUnitCode = types.StringValue("new_code")
		if adUnitReplaceReservesCode(state, plan, false) {
			t.Error("a changed ad_unit_code is never reused by the create, so no collision")
		}
	})

	t.Run("replace with unknown planned code does not warn", func(t *testing.T) {
		// An auto-assigned (config-null) ad_unit_code can plan as unknown on a
		// replace; the create then omits it and GAM assigns a fresh code, so
		// nothing collides. (When UseStateForUnknown instead retains the old
		// known code, plan==state and the "parent change" case above warns.)
		state, plan := base(), base()
		plan.ParentAdUnit = types.StringValue("networks/123456/adUnits/2")
		plan.AdUnitCode = types.StringUnknown()
		if adUnitReplaceReservesCode(state, plan, false) {
			t.Error("an unknown planned code is omitted by the create, so GAM auto-assigns and nothing collides")
		}
	})

	t.Run("framework-flagged replace reserves code", func(t *testing.T) {
		state, plan := base(), base() // no attribute diff, but framework forces replace
		if !adUnitReplaceReservesCode(state, plan, true) {
			t.Error("framework RequiresReplace with a code set must warn")
		}
	})

	t.Run("plain update does not warn", func(t *testing.T) {
		state, plan := base(), base() // identical immutables, no framework replace
		if adUnitReplaceReservesCode(state, plan, false) {
			t.Error("a non-replace update must not warn")
		}
	})

	t.Run("skip_archive_on_destroy suppresses the warning", func(t *testing.T) {
		state, plan := base(), base()
		state.SkipArchiveOnDestroy = types.BoolValue(true)
		plan.ParentAdUnit = types.StringValue("networks/123456/adUnits/2")
		if adUnitReplaceReservesCode(state, plan, false) {
			t.Error("skip_archive_on_destroy means no archive, so no reservation, so no warning")
		}
	})

	t.Run("empty ad_unit_code does not warn", func(t *testing.T) {
		state, plan := base(), base()
		state.AdUnitCode = types.StringNull()
		plan.AdUnitCode = types.StringNull()
		plan.ParentAdUnit = types.StringValue("networks/123456/adUnits/2")
		if adUnitReplaceReservesCode(state, plan, false) {
			t.Error("no reserved code means nothing to collide with")
		}
	})
}

// modelForModifyPlan returns a fully-populated model safe to Set into framework
// state/plan (all list attributes carry their element type).
func modelForModifyPlan() adUnitResourceModel {
	return adUnitResourceModel{
		adUnitModel: adUnitModel{
			ID:           types.StringValue("networks/123456/adUnits/456"),
			AdUnitID:     types.StringValue("456"),
			ParentAdUnit: types.StringValue("networks/123456/adUnits/1"),
			DisplayName:  types.StringValue("Homepage Leaderboard"),
			AdUnitCode:   types.StringValue("homepage_leaderboard"),
			AppliedTeams: types.ListNull(types.StringType),
			Teams:        types.ListNull(types.StringType),
		},
		SkipArchiveOnDestroy: types.BoolValue(false),
	}
}

// TestModifyPlanWarnsOnReplaceThroughFramework drives ModifyPlan through the
// framework state/plan types (the required end-to-end exercise): a parent change
// forces a replace and must emit exactly the reserved-code warning.
func TestModifyPlanWarnsOnReplaceThroughFramework(t *testing.T) {
	ctx := context.Background()
	sch := adUnitTestSchema(t)

	state := modelForModifyPlan()
	plan := modelForModifyPlan()
	plan.ParentAdUnit = types.StringValue("networks/123456/adUnits/2")

	stateObj := tfsdk.State{Schema: sch}
	if d := stateObj.Set(ctx, &state); d.HasError() {
		t.Fatalf("set state: %v", d)
	}
	planObj := tfsdk.Plan{Schema: sch}
	if d := planObj.Set(ctx, &plan); d.HasError() {
		t.Fatalf("set plan: %v", d)
	}

	r := NewAdUnitResource().(resource.ResourceWithModifyPlan)
	resp := &resource.ModifyPlanResponse{
		Plan:            planObj,
		RequiresReplace: path.Paths{path.Root("parent_ad_unit")},
	}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: stateObj, Plan: planObj}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("ModifyPlan errored: %v", resp.Diagnostics)
	}
	warns := resp.Diagnostics.Warnings()
	if len(warns) != 1 {
		t.Fatalf("warnings = %d, want exactly 1: %v", len(warns), warns)
	}
	detail := warns[0].Detail()
	for _, want := range []string{"homepage_leaderboard", "skip_archive_on_destroy", "terraform import"} {
		if !strings.Contains(detail, want) {
			t.Errorf("warning detail missing %q:\n%s", want, detail)
		}
	}
}

// TestModifyPlanNoWarningOnPlainUpdate: an in-place update (no immutable change,
// no framework replace) must not warn.
func TestModifyPlanNoWarningOnPlainUpdate(t *testing.T) {
	ctx := context.Background()
	sch := adUnitTestSchema(t)

	state := modelForModifyPlan()
	plan := modelForModifyPlan()
	plan.DisplayName = types.StringValue("Renamed") // mutable-only change

	stateObj := tfsdk.State{Schema: sch}
	if d := stateObj.Set(ctx, &state); d.HasError() {
		t.Fatalf("set state: %v", d)
	}
	planObj := tfsdk.Plan{Schema: sch}
	if d := planObj.Set(ctx, &plan); d.HasError() {
		t.Fatalf("set plan: %v", d)
	}

	r := NewAdUnitResource().(resource.ResourceWithModifyPlan)
	resp := &resource.ModifyPlanResponse{Plan: planObj}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: stateObj, Plan: planObj}, resp)

	if n := len(resp.Diagnostics.Warnings()); n != 0 {
		t.Errorf("warnings = %d, want 0 for a plain update: %v", n, resp.Diagnostics.Warnings())
	}
}

// TestModifyPlanNoWarningOnCreateOrDestroy: create (null prior state) and destroy
// (null plan) must early-return without warnings.
func TestModifyPlanNoWarningOnCreateOrDestroy(t *testing.T) {
	ctx := context.Background()
	sch := adUnitTestSchema(t)
	model := modelForModifyPlan()
	obj := tfsdk.State{Schema: sch}
	if d := obj.Set(ctx, &model); d.HasError() {
		t.Fatalf("set: %v", d)
	}
	planObj := tfsdk.Plan{Schema: sch}
	if d := planObj.Set(ctx, &model); d.HasError() {
		t.Fatalf("set plan: %v", d)
	}
	r := NewAdUnitResource().(resource.ResourceWithModifyPlan)

	t.Run("create: null prior state", func(t *testing.T) {
		resp := &resource.ModifyPlanResponse{Plan: planObj}
		// A fresh tfsdk.State has a null Raw (create has no prior state).
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: tfsdk.State{Schema: sch}, Plan: planObj}, resp)
		if n := len(resp.Diagnostics.Warnings()); n != 0 {
			t.Errorf("warnings = %d on create, want 0", n)
		}
	})

	t.Run("destroy: null plan", func(t *testing.T) {
		resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sch}}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: obj, Plan: tfsdk.Plan{Schema: sch}}, resp)
		if n := len(resp.Diagnostics.Warnings()); n != 0 {
			t.Errorf("warnings = %d on destroy, want 0", n)
		}
	})
}
