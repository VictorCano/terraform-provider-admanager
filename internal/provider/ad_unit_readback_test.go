package provider

// Tests for issue #1: on old (AdSense-linked) networks the REST create/read
// response OMITS the applied twin (appliedTargetWindow / appliedAdsenseEnabled)
// even though the write was accepted — only the effective twin is echoed. The
// absent->null mapping then contradicts the plan and Terraform rejects the apply
// with "inconsistent result after apply". The resource preserves the prior value
// ONLY when the effective twin corroborates it is observably in force; a genuine
// divergence still surfaces honestly.
//
// All tests run against local httptest servers with a static fake token.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/oauth2"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// oldNetworkCreateBody is the exact old-network create response shape from issue
// #1: effectiveTargetWindow is echoed but appliedTargetWindow is absent.
const oldNetworkCreateBody = `{
	"name": "networks/123456/adUnits/999",
	"adUnitId": "999",
	"displayName": "CZMB Widescreen Footer",
	"parentAdUnit": "networks/123456/adUnits/1",
	"adUnitCode": "czmb_widescreen_footer",
	"effectiveTargetWindow": "BLANK",
	"status": "ACTIVE",
	"hasChildren": false,
	"updateTime": "2026-07-05T12:00:00Z",
	"adUnitSizes": [
		{"size": {"width": 730, "height": 50, "sizeType": "PIXEL"}, "environmentType": "BROWSER"}
	]
}`

func newAdUnitResource(t *testing.T, srv *httptest.Server) *adUnitResource {
	t.Helper()
	c, err := client.New(context.Background(), client.Config{
		NetworkCode: "123456",
		BaseURL:     srv.URL,
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}),
		// One attempt keeps retryable-GET fixtures (e.g. a 503 holder lookup) from
		// spinning through the exponential backoff and slowing the suite; the retry
		// policy itself is covered in internal/client.
		RetryMaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return &adUnitResource{client: c}
}

// planForFooter builds the plan for the issue #1 config: target_window = "BLANK".
func planForFooter(t *testing.T) tfsdk.Plan {
	t.Helper()
	return newAdUnitPlan(t, adUnitResourceModel{
		adUnitModel: adUnitModel{
			ParentAdUnit: types.StringValue("networks/123456/adUnits/1"),
			DisplayName:  types.StringValue("CZMB Widescreen Footer"),
			AdUnitCode:   types.StringValue("czmb_widescreen_footer"),
			TargetWindow: types.StringValue("BLANK"),
			Sizes: []sizeModel{{
				Width: types.Int64Value(730), Height: types.Int64Value(50),
				SizeType: types.StringValue("PIXEL"), EnvironmentType: types.StringValue("BROWSER"),
			}},
			AppliedTeams: types.ListNull(types.StringType),
			Teams:        types.ListNull(types.StringType),
		},
		SkipArchiveOnDestroy: types.BoolValue(false),
	})
}

// TestCreatePreservesTargetWindowWhenResponseOmitsApplied is the core issue #1
// regression: the create response omits appliedTargetWindow but effective is
// "BLANK" (== plan). target_window must stay "BLANK", not collapse to null.
func TestCreatePreservesTargetWindowWhenResponseOmitsApplied(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(oldNetworkCreateBody))
	}))
	defer srv.Close()

	r := newAdUnitResource(t, srv)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: adUnitTestSchema(t)}}
	r.Create(ctx, resource.CreateRequest{Plan: planForFooter(t)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	var got adUnitResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.TargetWindow.IsNull() || got.TargetWindow.ValueString() != "BLANK" {
		t.Errorf("target_window = %v, want preserved \"BLANK\" (effective corroborates the applied write)", got.TargetWindow)
	}
	if got.EffectiveTargetWindow.ValueString() != "BLANK" {
		t.Errorf("effective_target_window = %v, want BLANK", got.EffectiveTargetWindow)
	}
}

// TestCreateHonestNullWhenEffectiveDiverges is the honest-drift guard: when the
// response omits appliedTargetWindow AND the effective twin does NOT match the
// planned value, nothing is preserved — the field maps to null so the real
// divergence surfaces instead of being papered over.
func TestCreateHonestNullWhenEffectiveDiverges(t *testing.T) {
	ctx := context.Background()
	// Plan wants BLANK, but effective comes back TOP (a genuine divergence).
	body := strings.Replace(oldNetworkCreateBody, `"effectiveTargetWindow": "BLANK"`, `"effectiveTargetWindow": "TOP"`, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	r := newAdUnitResource(t, srv)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: adUnitTestSchema(t)}}
	r.Create(ctx, resource.CreateRequest{Plan: planForFooter(t)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	var got adUnitResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if !got.TargetWindow.IsNull() {
		t.Errorf("target_window = %v, want null (effective TOP != planned BLANK => no preservation)", got.TargetWindow)
	}
}

// TestReadPreservesTargetWindowWhenResponseOmitsApplied replays the same
// old-network shape on a refresh: the prior state holds "BLANK", the read
// response omits appliedTargetWindow but effective corroborates it, so the read
// keeps "BLANK" instead of proposing spurious drift to null.
func TestReadPreservesTargetWindowWhenResponseOmitsApplied(t *testing.T) {
	ctx := context.Background()
	readBody := `{
		"name": "networks/123456/adUnits/999",
		"adUnitId": "999",
		"displayName": "CZMB Widescreen Footer",
		"parentAdUnit": "networks/123456/adUnits/1",
		"adUnitCode": "czmb_widescreen_footer",
		"effectiveTargetWindow": "BLANK",
		"status": "ACTIVE",
		"updateTime": "2026-07-05T12:00:00Z"
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(readBody))
	}))
	defer srv.Close()

	prior := adUnitResourceModel{
		adUnitModel: adUnitModel{
			ID:           types.StringValue("networks/123456/adUnits/999"),
			ParentAdUnit: types.StringValue("networks/123456/adUnits/1"),
			DisplayName:  types.StringValue("CZMB Widescreen Footer"),
			AdUnitCode:   types.StringValue("czmb_widescreen_footer"),
			TargetWindow: types.StringValue("BLANK"),
			AppliedTeams: types.ListNull(types.StringType),
			Teams:        types.ListNull(types.StringType),
		},
		SkipArchiveOnDestroy: types.BoolValue(false),
	}
	r := newAdUnitResource(t, srv)
	resp := &resource.ReadResponse{State: newAdUnitState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newAdUnitState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", resp.Diagnostics)
	}
	var got adUnitResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.TargetWindow.IsNull() || got.TargetWindow.ValueString() != "BLANK" {
		t.Errorf("target_window = %v, want preserved \"BLANK\" on refresh", got.TargetWindow)
	}
}

// TestReadHonestNullWhenPriorTargetWindowNull is the boundary guard: if the
// prior value is already null (nothing to corroborate), an omitted
// appliedTargetWindow must stay null — the fallback never invents a value.
func TestReadHonestNullWhenPriorTargetWindowNull(t *testing.T) {
	ctx := context.Background()
	readBody := `{
		"name": "networks/123456/adUnits/999",
		"parentAdUnit": "networks/123456/adUnits/1",
		"displayName": "Bare",
		"adUnitCode": "bare",
		"effectiveTargetWindow": "TOP",
		"status": "ACTIVE"
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(readBody))
	}))
	defer srv.Close()

	prior := adUnitResourceModel{
		adUnitModel: adUnitModel{
			ID:           types.StringValue("networks/123456/adUnits/999"),
			ParentAdUnit: types.StringValue("networks/123456/adUnits/1"),
			DisplayName:  types.StringValue("Bare"),
			AdUnitCode:   types.StringValue("bare"),
			TargetWindow: types.StringNull(),
			AppliedTeams: types.ListNull(types.StringType),
			Teams:        types.ListNull(types.StringType),
		},
		SkipArchiveOnDestroy: types.BoolValue(false),
	}
	r := newAdUnitResource(t, srv)
	resp := &resource.ReadResponse{State: newAdUnitState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newAdUnitState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", resp.Diagnostics)
	}
	var got adUnitResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if !got.TargetWindow.IsNull() {
		t.Errorf("target_window = %v, want null (no prior value to corroborate)", got.TargetWindow)
	}
}

// TestReconcileOmittedAppliedFields exercises the sibling audit directly:
// applied_adsense_enabled has an effective twin (effectiveAdsenseEnabled) and
// gets the same corroborated fallback; smart_size_mode has NO effective twin and
// its omit-on-old-networks behavior is intentionally left unchanged.
func TestReconcileOmittedAppliedFields(t *testing.T) {
	t.Run("adsense preserved when effective corroborates prior", func(t *testing.T) {
		// API omits appliedAdsenseEnabled; effective == true == prior applied.
		au := &client.AdUnit{
			Name:                    "networks/123456/adUnits/999",
			EffectiveAdsenseEnabled: true,
		}
		mapped := adUnitModel{AppliedAdsenseEnabled: types.BoolNull()} // absent->null from honest mapping
		prior := adUnitModel{AppliedAdsenseEnabled: types.BoolValue(true)}
		reconcileOmittedAppliedFields(&mapped, au, prior)
		if mapped.AppliedAdsenseEnabled.IsNull() || !mapped.AppliedAdsenseEnabled.ValueBool() {
			t.Errorf("applied_adsense_enabled = %v, want preserved true", mapped.AppliedAdsenseEnabled)
		}
	})

	t.Run("adsense honest null when effective diverges", func(t *testing.T) {
		au := &client.AdUnit{EffectiveAdsenseEnabled: false} // effective false != prior true
		mapped := adUnitModel{AppliedAdsenseEnabled: types.BoolNull()}
		prior := adUnitModel{AppliedAdsenseEnabled: types.BoolValue(true)}
		reconcileOmittedAppliedFields(&mapped, au, prior)
		if !mapped.AppliedAdsenseEnabled.IsNull() {
			t.Errorf("applied_adsense_enabled = %v, want null (effective diverged)", mapped.AppliedAdsenseEnabled)
		}
	})

	t.Run("smart_size_mode never preserved (no effective twin)", func(t *testing.T) {
		au := &client.AdUnit{SmartSizeMode: ""} // omitted on old network
		mapped := adUnitModel{SmartSizeMode: types.StringNull()}
		prior := adUnitModel{SmartSizeMode: types.StringValue("SMART_BANNER")}
		reconcileOmittedAppliedFields(&mapped, au, prior)
		if !mapped.SmartSizeMode.IsNull() {
			t.Errorf("smart_size_mode = %v, want left null (no effective twin to corroborate)", mapped.SmartSizeMode)
		}
	})

	t.Run("applied target window preserved when API sends it", func(t *testing.T) {
		// When the API DOES echo appliedTargetWindow, the fallback must not touch
		// the already-correct mapped value.
		au := &client.AdUnit{AppliedTargetWindow: "TOP", EffectiveTargetWindow: "TOP"}
		mapped := adUnitModel{TargetWindow: types.StringValue("TOP")}
		prior := adUnitModel{TargetWindow: types.StringValue("BLANK")}
		reconcileOmittedAppliedFields(&mapped, au, prior)
		if mapped.TargetWindow.ValueString() != "TOP" {
			t.Errorf("target_window = %v, want the API-returned TOP untouched", mapped.TargetWindow)
		}
	})
}
