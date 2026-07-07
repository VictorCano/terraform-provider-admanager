package provider

// P0-9: malformed-import-ID negatives, one per resource. ImportState must never
// panic and must emit a clean "Invalid import ID" diagnostic for IDs its parser
// rejects (empty, or a slash-bearing string that is not a full resource name).
//
// FLAGGED FOR VICTOR (current behavior, pinned here — not a crash, so not
// fixed): the normalizers only reject IDs that contain a slash but do NOT start
// with "networks/". A "networks/"-prefixed but structurally broken name (e.g.
// "networks//adUnits/", "networks/123/adUnits/abc") and a bare non-numeric token
// (e.g. "not-a-name") are accepted at import time and deferred to the follow-up
// Read, which 404s. If import-time validation should be stricter, that is a
// production change for Victor to decide; these cases are pinned as-is so a
// future tightening is a deliberate, visible diff.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// importIDCase pairs an import ID with whether the parser rejects it today.
type importIDCase struct {
	id       string
	wantDiag bool
}

// commonImportIDCases are shared across resources (collection names differ but
// the parser structure is identical). Callers prepend their own collection-name
// slash case.
func commonImportIDCases() []importIDCase {
	return []importIDCase{
		{"", true},                         // empty -> rejected
		{"   ", true},                      // whitespace only -> trimmed to empty -> rejected
		{"network/123456/adUnits/1", true}, // singular "network" typo (slash, no "networks/" prefix)
		// Accepted today, deferred to Read (see file header):
		{"not-a-name", false},
		{"networks/123/adUnits/abc", false},
	}
}

func assertImport(t *testing.T, id string, wantDiag bool, imp func(id string) *resource.ImportStateResponse) {
	t.Helper()
	resp := imp(id) // must never panic
	switch {
	case wantDiag && !resp.Diagnostics.HasError():
		t.Errorf("id %q: expected an Invalid import ID diagnostic, got none", id)
	case wantDiag:
		if got := resp.Diagnostics.Errors()[0].Summary(); got != "Invalid import ID" {
			t.Errorf("id %q: summary = %q, want %q", id, got, "Invalid import ID")
		}
	case !wantDiag && resp.Diagnostics.HasError():
		t.Errorf("id %q: unexpectedly rejected: %v", id, resp.Diagnostics)
	}
}

// noAPIServer fails the test if ImportState issues any HTTP call (it must only
// read the network code and set the id attribute).
func noAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("ImportState must not call the API; got %s %s", r.Method, r.URL.Path)
	}))
}

func TestAdUnitImportStateMalformedID(t *testing.T) {
	ctx := context.Background()
	srv := noAPIServer(t)
	defer srv.Close()
	r := &adUnitResource{client: newAdUnitTestClient(t, srv)}

	cases := append(commonImportIDCases(), importIDCase{"adUnits/123", true})
	for _, tc := range cases {
		assertImport(t, tc.id, tc.wantDiag, func(id string) *resource.ImportStateResponse {
			resp := &resource.ImportStateResponse{State: newAdUnitState(t, adUnitResourceModel{
				adUnitModel: adUnitModel{
					AppliedTeams: types.ListNull(types.StringType),
					Teams:        types.ListNull(types.StringType),
				},
			})}
			r.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
			return resp
		})
	}
}

func TestPlacementImportStateMalformedID(t *testing.T) {
	ctx := context.Background()
	srv := noAPIServer(t)
	defer srv.Close()
	r := &placementResource{client: newAdUnitTestClient(t, srv)}

	cases := append(commonImportIDCases(), importIDCase{"placements/123", true})
	for _, tc := range cases {
		assertImport(t, tc.id, tc.wantDiag, func(id string) *resource.ImportStateResponse {
			resp := &resource.ImportStateResponse{State: newPlacementState(t, placementResourceModel{
				TargetedAdUnits: types.SetNull(types.StringType),
			})}
			r.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
			return resp
		})
	}
}

func TestCustomTargetingKeyImportStateMalformedID(t *testing.T) {
	ctx := context.Background()
	srv := noAPIServer(t)
	defer srv.Close()
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}

	cases := append(commonImportIDCases(), importIDCase{"customTargetingKeys/123", true})
	for _, tc := range cases {
		assertImport(t, tc.id, tc.wantDiag, func(id string) *resource.ImportStateResponse {
			resp := &resource.ImportStateResponse{State: newCustomTargetingKeyState(t, customTargetingKeyResourceModel{})}
			r.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
			return resp
		})
	}
}

func TestCustomTargetingValueImportStateMalformedID(t *testing.T) {
	ctx := context.Background()
	srv := noAPIServer(t)
	defer srv.Close()
	r := newValueTestResource(t, srv)

	cases := append(commonImportIDCases(), importIDCase{"customTargetingValues/123", true})
	for _, tc := range cases {
		assertImport(t, tc.id, tc.wantDiag, func(id string) *resource.ImportStateResponse {
			resp := &resource.ImportStateResponse{State: newValueState(t, customTargetingValueResourceModel{})}
			r.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
			return resp
		})
	}
}
