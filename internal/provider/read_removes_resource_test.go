package provider

// P0-1: Read's not-found -> RemoveResource branch, one per resource. This is
// the mechanism the whole provider relies on to represent out-of-band deletion:
// when the API 404s a GET during refresh, the resource must be dropped from
// state (resp.State.Raw becomes null) with NO error diagnostics, so Terraform
// plans a clean recreate instead of erroring or leaving a ghost in state.
//
// All tests run against local httptest servers returning the real GAM error
// envelope shape; a 404 is not retried, so no rate-limit backoff is incurred.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// notFoundHandler writes a GAM 404 error envelope for the named entity.
func notFoundHandler(message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"` + message + `"}}`))
	}
}

func TestAdUnitReadRemovesResourceWhenGone(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(notFoundHandler("AdUnit not found"))
	defer srv.Close()

	prior, _ := adUnitAPIToModel(ctx, apiAdUnit(), types.BoolValue(false))
	r := &adUnitResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.ReadResponse{State: newAdUnitState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newAdUnitState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a 404 refresh must not error; diags = %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state must be removed (null) when the ad unit is gone from Ad Manager")
	}
}

func TestPlacementReadRemovesResourceWhenGone(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(notFoundHandler("Placement not found"))
	defer srv.Close()

	prior, _ := placementAPIToModel(ctx, apiPlacement(), types.BoolValue(false))
	r := &placementResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.ReadResponse{State: newPlacementState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newPlacementState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a 404 refresh must not error; diags = %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state must be removed (null) when the placement is gone from Ad Manager")
	}
}

func TestCustomTargetingKeyReadRemovesResourceWhenGone(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(notFoundHandler("CustomTargetingKey not found"))
	defer srv.Close()

	prior, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.ReadResponse{State: newCustomTargetingKeyState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newCustomTargetingKeyState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a 404 refresh must not error; diags = %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state must be removed (null) when the custom targeting key is gone from Ad Manager")
	}
}

func TestCustomTargetingValueReadRemovesResourceWhenGone(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(notFoundHandler("CustomTargetingValue not found"))
	defer srv.Close()

	prior := customTargetingValueAPIToModel(apiCustomTargetingValue(), types.BoolValue(false))
	r := newValueTestResource(t, srv)
	resp := &resource.ReadResponse{State: newValueState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newValueState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a 404 refresh must not error; diags = %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state must be removed (null) when the custom targeting value is gone from Ad Manager")
	}
}
