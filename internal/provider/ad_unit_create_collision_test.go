package provider

// Tests for issue #2 (create-time collision diagnosis): when an ad_unit create
// fails with 400 INVALID_ARGUMENT and the plan pinned an ad_unit_code, the
// provider best-effort looks up who currently holds that code (archived units
// included) and names the holder plus the recovery paths. A lookup failure falls
// back to the plain, detail-enriched diagnostic without masking the real error.
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
)

// invalidArgumentBody is the opaque 400 the API returns when the ad_unit_code is
// already reserved by an archived unit (issue #2).
const invalidArgumentBody = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"An error occurred. Please try again later."}}`

// TestCreateCollisionNamesArchivedHolder: create 400s, the by-code list returns
// the ARCHIVED holder, and the diagnostic must name it and the recovery paths.
func TestCreateCollisionNamesArchivedHolder(t *testing.T) {
	ctx := context.Background()
	var listCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(invalidArgumentBody))
		case http.MethodGet:
			listCalls++
			if got := r.URL.Query().Get("filter"); got != `adUnitCode = "czmb_widescreen_footer"` {
				t.Errorf("list filter = %q, want adUnitCode = \"czmb_widescreen_footer\"", got)
			}
			// The list returns an ARCHIVED unit: the holder must be discoverable
			// even though it is archived (no status filter excludes it).
			_, _ = w.Write([]byte(`{"adUnits":[{
				"name":"networks/123456/adUnits/23360120651",
				"adUnitId":"23360120651",
				"displayName":"CZMB Widescreen Footer",
				"adUnitCode":"czmb_widescreen_footer",
				"status":"ARCHIVED"
			}]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := newAdUnitResource(t, srv)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: adUnitTestSchema(t)}}
	r.Create(ctx, resource.CreateRequest{Plan: planForFooter(t)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected a create error, got none")
	}
	if listCalls != 1 {
		t.Errorf("list lookups = %d, want exactly 1 best-effort holder lookup", listCalls)
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{
		"czmb_widescreen_footer",
		"networks/123456/adUnits/23360120651",
		"ARCHIVED",
		"terraform import",
		"different ad_unit_code",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("diagnostic detail missing %q:\n%s", want, detail)
		}
	}
}

// TestCreateCollisionLookupFailsFallsBack: create 400s, but the holder lookup
// itself fails (503). The diagnostic must fall back to the plain detail-enriched
// error — the lookup failure must never mask the original create error.
func TestCreateCollisionLookupFailsFallsBack(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(invalidArgumentBody))
		case http.MethodGet:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"status":"UNAVAILABLE","message":"try later"}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := newAdUnitResource(t, srv)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: adUnitTestSchema(t)}}
	r.Create(ctx, resource.CreateRequest{Plan: planForFooter(t)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected a create error, got none")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	// Falls back to the base diagnostic (the create error), not the holder message.
	if !strings.Contains(detail, "INVALID_ARGUMENT") {
		t.Errorf("fallback diagnostic should carry the create error:\n%s", detail)
	}
	if strings.Contains(detail, "is already held by") {
		t.Errorf("holder message must not appear when the lookup failed:\n%s", detail)
	}
}

// TestCreateCollisionSkippedWithoutCode: a 400 with no ad_unit_code in the plan
// must NOT trigger a holder lookup (nothing to look up) — just the base error.
func TestCreateCollisionSkippedWithoutCode(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			t.Errorf("no holder lookup expected when ad_unit_code is unset; got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(invalidArgumentBody))
	}))
	defer srv.Close()

	plan := newAdUnitPlan(t, adUnitResourceModel{
		adUnitModel: adUnitModel{
			ParentAdUnit: types.StringValue("networks/123456/adUnits/1"),
			DisplayName:  types.StringValue("No Code"),
			AppliedTeams: types.ListNull(types.StringType),
			Teams:        types.ListNull(types.StringType),
		},
		SkipArchiveOnDestroy: types.BoolValue(false),
	})
	r := newAdUnitResource(t, srv)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: adUnitTestSchema(t)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected a create error, got none")
	}
}
