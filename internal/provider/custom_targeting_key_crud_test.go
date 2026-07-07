package provider

// P0-2: in-process Create / Read / mask-Update happy paths for the custom
// targeting key resource. These success paths were acceptance-only until now
// (only Delete and the empty-mask Update branch had unit tests). Every fake
// serves realistic GAM key JSON (shapes copied from apiCustomTargetingKey).

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

func newCustomTargetingKeyPlan(t *testing.T, m customTargetingKeyResourceModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: customTargetingKeyTestSchema(t)}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building plan: %v", d)
	}
	return p
}

const createdCustomTargetingKeyJSON = `{
	"name": "networks/123456/customTargetingKeys/321",
	"customTargetingKeyId": "321",
	"adTagName": "genre",
	"displayName": "Genre",
	"type": "FREEFORM",
	"reportableType": "ON",
	"status": "ACTIVE"
}`

func TestCustomTargetingKeyCreatePersistsState(t *testing.T) {
	ctx := context.Background()
	var postCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/networks/123456/customTargetingKeys" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		postCalls++
		_, _ = w.Write([]byte(createdCustomTargetingKeyJSON))
	}))
	defer srv.Close()

	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	plan := newCustomTargetingKeyPlan(t, customTargetingKeyResourceModel{
		AdTagName:            types.StringValue("genre"),
		DisplayName:          types.StringValue("Genre"),
		Type:                 types.StringValue("FREEFORM"),
		ReportableType:       types.StringValue("ON"),
		SkipArchiveOnDestroy: types.BoolValue(false),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: customTargetingKeyTestSchema(t)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	if postCalls != 1 {
		t.Errorf("postCalls = %d, want 1", postCalls)
	}
	var got customTargetingKeyResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.ID.ValueString() != "networks/123456/customTargetingKeys/321" || got.CustomTargetingKeyID.ValueString() != "321" {
		t.Errorf("id/custom_targeting_key_id = %q / %q", got.ID.ValueString(), got.CustomTargetingKeyID.ValueString())
	}
	if got.Type.ValueString() != "FREEFORM" || got.ReportableType.ValueString() != "ON" || got.Status.ValueString() != "ACTIVE" {
		t.Errorf("computed/settable fields not populated from create response: %+v", got)
	}
}

func TestCustomTargetingKeyReadRefreshesState(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/networks/123456/customTargetingKeys/321" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		// The live object drifted: display name and reportable type differ. The key
		// stays ACTIVE — an INACTIVE key is a separate path (Read removes it from
		// state; see TestCustomTargetingKeyReadRemovesResourceWhenInactive), so a
		// live drift test must keep the key active to exercise honest field refresh.
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/customTargetingKeys/321",
			"customTargetingKeyId": "321",
			"adTagName": "genre",
			"displayName": "Music Genre",
			"type": "FREEFORM",
			"reportableType": "CUSTOM_DIMENSION",
			"status": "ACTIVE"
		}`))
	}))
	defer srv.Close()

	prior, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(true))
	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.ReadResponse{State: newCustomTargetingKeyState(t, prior)}
	r.Read(ctx, resource.ReadRequest{State: newCustomTargetingKeyState(t, prior)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", resp.Diagnostics)
	}
	var got customTargetingKeyResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.DisplayName.ValueString() != "Music Genre" || got.ReportableType.ValueString() != "CUSTOM_DIMENSION" ||
		got.Status.ValueString() != "ACTIVE" {
		t.Errorf("read did not refresh drifted fields: %+v", got)
	}
	if !got.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy must be carried through the read unchanged")
	}
}

// TestCustomTargetingKeyUpdateWithMaskAppliesPatch drives a display-name-only
// change and asserts the PATCH carries an exact updateMask of just displayName.
// The immutable adTagName (and unchanged type/reportableType) is kept out of the
// MASK by buildCustomTargetingKeyUpdateMask; it still rides along in the request
// body because the client serializes the whole object, and the updateMask — not
// the body — is what selects the fields the API actually applies.
func TestCustomTargetingKeyUpdateWithMaskAppliesPatch(t *testing.T) {
	ctx := context.Background()
	var gotMask string
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/networks/123456/customTargetingKeys/321" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotMask = r.URL.Query().Get("updateMask")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshaling PATCH body: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"name": "networks/123456/customTargetingKeys/321",
			"customTargetingKeyId": "321",
			"adTagName": "genre",
			"displayName": "Music Genre",
			"type": "FREEFORM",
			"reportableType": "ON",
			"status": "ACTIVE"
		}`))
	}))
	defer srv.Close()

	state, _ := customTargetingKeyAPIToModel(apiCustomTargetingKey(), types.BoolValue(false))
	plan := state
	plan.DisplayName = types.StringValue("Music Genre")
	plan.Status = types.StringUnknown() // plain-computed: Unknown in the plan.

	r := &customTargetingKeyResource{client: newAdUnitTestClient(t, srv)}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: customTargetingKeyTestSchema(t)}}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  newCustomTargetingKeyPlan(t, plan),
		State: newCustomTargetingKeyState(t, state),
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
	var got customTargetingKeyResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.DisplayName.ValueString() != "Music Genre" {
		t.Errorf("display_name = %q, want Music Genre", got.DisplayName.ValueString())
	}
}
