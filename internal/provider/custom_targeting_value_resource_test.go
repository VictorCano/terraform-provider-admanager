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
	"github.com/VictorCano/terraform-provider-admanager/internal/soap"
)

// --- fixtures ----------------------------------------------------------------

// soapCreateResponse echoes a created value with a server-assigned id.
const soapCreateResponse = `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <createCustomTargetingValuesResponse xmlns="https://www.google.com/apis/ads/publisher/v202605">
      <rval>
        <customTargetingKeyId>321</customTargetingKeyId>
        <id>555</id>
        <name>honda</name>
        <displayName>Honda</displayName>
        <matchType>EXACT</matchType>
        <status>ACTIVE</status>
      </rval>
    </createCustomTargetingValuesResponse>
  </soap:Body>
</soap:Envelope>`

const soapUpdateResponse = `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <updateCustomTargetingValuesResponse xmlns="https://www.google.com/apis/ads/publisher/v202605">
      <rval>
        <customTargetingKeyId>321</customTargetingKeyId>
        <id>555</id>
        <name>honda</name>
        <displayName>Honda Updated</displayName>
        <matchType>EXACT</matchType>
        <status>ACTIVE</status>
      </rval>
    </updateCustomTargetingValuesResponse>
  </soap:Body>
</soap:Envelope>`

const soapActionResponse = `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <performCustomTargetingValueActionResponse xmlns="https://www.google.com/apis/ads/publisher/v202605">
      <rval><numChanges>1</numChanges></rval>
    </performCustomTargetingValueActionResponse>
  </soap:Body>
</soap:Envelope>`

const soapFaultResponse = `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <soap:Body>
    <soap:Fault>
      <faultcode>soap:Server</faultcode>
      <faultstring>[CommonError.NOT_FOUND @ ]</faultstring>
      <detail>
        <ApiExceptionFault xmlns="https://www.google.com/apis/ads/publisher/v202605" xsi:type="ApiException">
          <message>[CommonError.NOT_FOUND @ ]</message>
          <errors xsi:type="CommonError"><errorString>CommonError.NOT_FOUND</errorString><reason>NOT_FOUND</reason></errors>
        </ApiExceptionFault>
      </detail>
    </soap:Fault>
  </soap:Body>
</soap:Envelope>`

func restValueJSON(status string) string {
	return `{
		"name": "networks/123456/customTargetingValues/555",
		"customTargetingKey": "networks/123456/customTargetingKeys/321",
		"adTagName": "honda",
		"displayName": "Honda",
		"matchType": "EXACT",
		"status": "` + status + `"
	}`
}

func isSOAPRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.Contains(r.URL.Path, "CustomTargetingService")
}

// --- helpers -----------------------------------------------------------------

func newValueTestResource(t *testing.T, srv *httptest.Server) *customTargetingValueResource {
	t.Helper()
	restClient := newAdUnitTestClient(t, srv)
	soapClient := soap.NewClient(soap.Config{
		HTTPClient:      restClient.HTTPClient(),
		Limiter:         restClient.Limiter(),
		NetworkCode:     restClient.NetworkCode(),
		ApplicationName: "terraform-provider-admanager/test",
		BaseURL:         srv.URL,
	})
	return &customTargetingValueResource{client: restClient, soap: soapClient}
}

func valueTestSchema(t *testing.T) rschema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewCustomTargetingValueResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func newValueState(t *testing.T, m customTargetingValueResourceModel) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: valueTestSchema(t)}
	if d := st.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building state: %v", d)
	}
	return st
}

func newValuePlan(t *testing.T, m customTargetingValueResourceModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: valueTestSchema(t)}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building plan: %v", d)
	}
	return p
}

// apiCustomTargetingValue is a representative REST response used by mapping tests.
func apiCustomTargetingValue() *client.CustomTargetingValue {
	return &client.CustomTargetingValue{
		Name:               "networks/123456/customTargetingValues/555",
		CustomTargetingKey: "networks/123456/customTargetingKeys/321",
		AdTagName:          "honda",
		DisplayName:        "Honda",
		MatchType:          "EXACT",
		Status:             "ACTIVE",
	}
}

// --- schema / metadata -------------------------------------------------------

func TestCustomTargetingValueResourceMetadata(t *testing.T) {
	r := NewCustomTargetingValueResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "admanager"}, resp)
	if resp.TypeName != "admanager_custom_targeting_value" {
		t.Errorf("TypeName = %q, want admanager_custom_targeting_value", resp.TypeName)
	}
}

func TestCustomTargetingValueResourceSchema(t *testing.T) {
	resp := &resource.SchemaResponse{}
	NewCustomTargetingValueResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	attrs := resp.Schema.Attributes

	for _, name := range []string{
		"id", "custom_targeting_value_id", "custom_targeting_key", "ad_tag_name",
		"display_name", "match_type", "status", "skip_archive_on_destroy",
	} {
		if _, ok := attrs[name]; !ok {
			t.Errorf("schema missing attribute %q", name)
		}
	}
	for _, name := range []string{"custom_targeting_key", "ad_tag_name", "match_type"} {
		if a := attrs[name]; !a.IsRequired() {
			t.Errorf("%s must be required", name)
		}
	}
	if a := attrs["display_name"]; !a.IsOptional() {
		t.Error("display_name must be optional")
	}
	for _, name := range []string{"id", "custom_targeting_value_id", "status"} {
		if a := attrs[name]; !a.IsComputed() {
			t.Errorf("%s must be computed", name)
		}
	}
	// The three immutable fields must force replacement.
	for _, name := range []string{"custom_targeting_key", "ad_tag_name", "match_type"} {
		sa, ok := attrs[name].(rschema.StringAttribute)
		if !ok {
			t.Fatalf("%s should be a StringAttribute", name)
		}
		if len(sa.PlanModifiers) == 0 {
			t.Errorf("%s must carry a RequiresReplace plan modifier (it is immutable)", name)
		}
	}
	// display_name is mutable: no RequiresReplace.
	dn, ok := attrs["display_name"].(rschema.StringAttribute)
	if !ok {
		t.Fatal("display_name should be a StringAttribute")
	}
	if len(dn.PlanModifiers) != 0 {
		t.Error("display_name must NOT force replacement: it is the only mutable field")
	}
	// custom_targeting_key must carry a validator constraining it to the full key
	// resource name so it survives the REST read-back comparison.
	if ck := attrs["custom_targeting_key"].(rschema.StringAttribute); len(ck.Validators) == 0 {
		t.Error("custom_targeting_key must carry a resource-name validator")
	}
	// ad_tag_name has the 40-char validator; match_type has the enum validator.
	if at := attrs["ad_tag_name"].(rschema.StringAttribute); len(at.Validators) == 0 {
		t.Error("ad_tag_name must carry a length validator")
	}
	if mt := attrs["match_type"].(rschema.StringAttribute); len(mt.Validators) == 0 {
		t.Error("match_type must carry an enum validator")
	}
}

// --- mapping -----------------------------------------------------------------

func TestCustomTargetingValueAPIToModelMapsAllFields(t *testing.T) {
	m := customTargetingValueAPIToModel(apiCustomTargetingValue(), types.BoolValue(true))
	if m.ID.ValueString() != "networks/123456/customTargetingValues/555" {
		t.Errorf("id = %q", m.ID.ValueString())
	}
	if m.CustomTargetingValueID.ValueString() != "555" {
		t.Errorf("custom_targeting_value_id = %q, want 555 (parsed from name)", m.CustomTargetingValueID.ValueString())
	}
	if m.CustomTargetingKey.ValueString() != "networks/123456/customTargetingKeys/321" {
		t.Errorf("custom_targeting_key = %q", m.CustomTargetingKey.ValueString())
	}
	if m.AdTagName.ValueString() != "honda" || m.DisplayName.ValueString() != "Honda" ||
		m.MatchType.ValueString() != "EXACT" || m.Status.ValueString() != "ACTIVE" {
		t.Errorf("mapped fields = %+v", m)
	}
	if !m.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy should be carried through unchanged")
	}
}

func TestCustomTargetingValueAPIToModelNullsAbsentDisplayName(t *testing.T) {
	v := apiCustomTargetingValue()
	v.DisplayName = ""
	m := customTargetingValueAPIToModel(v, types.BoolNull())
	if !m.DisplayName.IsNull() {
		t.Error("display_name should be null when the API omits it")
	}
}

func TestSOAPValueToModelBridgesIDAndKey(t *testing.T) {
	sv := &soap.Value{CustomTargetingKeyID: 321, ID: 555, Name: "honda", DisplayName: "Honda", MatchType: "EXACT", Status: "ACTIVE"}
	m := soapValueToModel(sv, "networks/123456/customTargetingValues/555", types.StringValue("networks/123456/customTargetingKeys/321"), types.BoolValue(false))
	if m.ID.ValueString() != "networks/123456/customTargetingValues/555" {
		t.Errorf("id = %q", m.ID.ValueString())
	}
	if m.CustomTargetingValueID.ValueString() != "555" {
		t.Errorf("custom_targeting_value_id = %q", m.CustomTargetingValueID.ValueString())
	}
	if m.CustomTargetingKey.ValueString() != "networks/123456/customTargetingKeys/321" {
		t.Errorf("custom_targeting_key = %q (must come from plan, SOAP has no resource name)", m.CustomTargetingKey.ValueString())
	}
	if m.AdTagName.ValueString() != "honda" {
		t.Errorf("ad_tag_name = %q (SOAP name maps to REST adTagName)", m.AdTagName.ValueString())
	}
}

// TestCustomTargetingKeyReferenceMustBeFullResourceName guards the state-consistency
// fix: custom_targeting_key must be the canonical key resource name, because the
// REST read-back after a SOAP create returns exactly that form. A bare numeric id
// (which soap.KeyIDFromResourceName otherwise tolerates) would canonicalize on
// read-back and trip "Provider produced inconsistent result after apply" on this
// Required attribute — and force perpetual replacement on later plans. The
// validator rejects it up front, before any value is created in Ad Manager.
func TestCustomTargetingKeyReferenceMustBeFullResourceName(t *testing.T) {
	// The canonical name the REST read returns must be accepted (round-trip safe).
	valid := []string{
		"networks/123456/customTargetingKeys/321",
		"networks/1/customTargetingKeys/9",
	}
	// Everything the finding flags as non-round-trippable must be rejected.
	invalid := []string{
		"321",                                  // bare numeric id
		"",                                     // empty
		"customTargetingKeys/321",              // partial
		"networks/123456/customTargetingKeys/", // missing id
		"networks//customTargetingKeys/321",    // missing network code
		"networks/123456/customTargetingKeys/321/",  // trailing slash
		"networks/123456/customTargetingKeys/abc",   // non-numeric id
		"networks/123456/customTargetingValues/321", // wrong collection (a value name)
	}
	for _, in := range valid {
		if !customTargetingKeyReferenceRegex.MatchString(in) {
			t.Errorf("expected %q to be accepted as a custom_targeting_key reference", in)
		}
	}
	for _, in := range invalid {
		if customTargetingKeyReferenceRegex.MatchString(in) {
			t.Errorf("expected %q to be rejected as a custom_targeting_key reference", in)
		}
	}
}

func TestNormalizeCustomTargetingValueName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"networks/123456/customTargetingValues/555", "networks/123456/customTargetingValues/555"},
		{"555", "networks/123456/customTargetingValues/555"},
		{"", ""},
		{"customTargetingValues/555", ""},
		{"network/123456/customTargetingValues/555", ""},
	}
	for _, tc := range cases {
		if got := normalizeCustomTargetingValueName(tc.in, "123456"); got != tc.want {
			t.Errorf("normalizeCustomTargetingValueName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- Create ------------------------------------------------------------------

func TestCustomTargetingValueCreateHybrid(t *testing.T) {
	ctx := context.Background()
	var soapCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isSOAPRequest(r):
			soapCalls++
			_, _ = w.Write([]byte(soapCreateResponse))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/networks/123456/customTargetingValues/555":
			getCalls++
			_, _ = w.Write([]byte(restValueJSON("ACTIVE")))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := newValueTestResource(t, srv)
	sch := valueTestSchema(t)
	plan := newValuePlan(t, customTargetingValueResourceModel{
		CustomTargetingKey:   types.StringValue("networks/123456/customTargetingKeys/321"),
		AdTagName:            types.StringValue("honda"),
		DisplayName:          types.StringValue("Honda"),
		MatchType:            types.StringValue("EXACT"),
		SkipArchiveOnDestroy: types.BoolValue(false),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	if soapCalls != 1 || getCalls != 1 {
		t.Errorf("soapCalls=%d getCalls=%d, want 1 and 1 (SOAP create then canonical REST read)", soapCalls, getCalls)
	}
	var got customTargetingValueResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.ID.ValueString() != "networks/123456/customTargetingValues/555" {
		t.Errorf("state id = %q", got.ID.ValueString())
	}
	if got.CustomTargetingValueID.ValueString() != "555" || got.Status.ValueString() != "ACTIVE" {
		t.Errorf("computed fields not populated from REST read: %+v", got)
	}
}

// TestCustomTargetingValueCreateReadBackFailureKeepsID is the state-corruption
// guard: when the SOAP create succeeds but the canonical REST read fails, the id
// must still land in state (never orphan the created value) and the failure is a
// warning, not an error that discards state.
func TestCustomTargetingValueCreateReadBackFailureKeepsID(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isSOAPRequest(r):
			_, _ = w.Write([]byte(soapCreateResponse))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"status":"UNAVAILABLE","message":"try later"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := newValueTestResource(t, srv)
	sch := valueTestSchema(t)
	plan := newValuePlan(t, customTargetingValueResourceModel{
		CustomTargetingKey:   types.StringValue("networks/123456/customTargetingKeys/321"),
		AdTagName:            types.StringValue("honda"),
		DisplayName:          types.StringValue("Honda"),
		MatchType:            types.StringValue("EXACT"),
		SkipArchiveOnDestroy: types.BoolValue(false),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create must not error when the value was created but not read back: %v", resp.Diagnostics)
	}
	if len(resp.Diagnostics.Warnings()) == 0 {
		t.Error("expected a warning that the value was created but not read back")
	}
	var got customTargetingValueResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.ID.ValueString() != "networks/123456/customTargetingValues/555" {
		t.Errorf("id not persisted after create-succeeded/read-failed: %q (would orphan the value)", got.ID.ValueString())
	}
	if got.CustomTargetingValueID.ValueString() != "555" || got.AdTagName.ValueString() != "honda" {
		t.Errorf("fallback state not populated from SOAP response: %+v", got)
	}
	if got.CustomTargetingKey.ValueString() != "networks/123456/customTargetingKeys/321" {
		t.Errorf("custom_targeting_key = %q, want the plan value", got.CustomTargetingKey.ValueString())
	}
}

func TestCustomTargetingValueCreateRejectsBadKey(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("no API call expected for an invalid key; got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	r := newValueTestResource(t, srv)
	sch := valueTestSchema(t)
	plan := newValuePlan(t, customTargetingValueResourceModel{
		CustomTargetingKey:   types.StringValue("networks/123456/customTargetingKeys/not-a-number"),
		AdTagName:            types.StringValue("honda"),
		MatchType:            types.StringValue("EXACT"),
		SkipArchiveOnDestroy: types.BoolValue(false),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error for an unparseable custom_targeting_key")
	}
}

// --- Update ------------------------------------------------------------------

func TestCustomTargetingValueUpdateDisplayName(t *testing.T) {
	ctx := context.Background()
	var soapCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isSOAPRequest(r):
			soapCalls++
			_, _ = w.Write([]byte(soapUpdateResponse))
		case r.Method == http.MethodGet:
			getCalls++
			body := restValueJSON("ACTIVE")
			body = strings.Replace(body, `"displayName": "Honda"`, `"displayName": "Honda Updated"`, 1)
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := newValueTestResource(t, srv)
	sch := valueTestSchema(t)
	state := customTargetingValueAPIToModel(apiCustomTargetingValue(), types.BoolValue(false))
	plan := state
	plan.DisplayName = types.StringValue("Honda Updated")
	plan.Status = types.StringUnknown()

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	r.Update(ctx, resource.UpdateRequest{Plan: newValuePlan(t, plan), State: newValueState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", resp.Diagnostics)
	}
	if soapCalls != 1 || getCalls != 1 {
		t.Errorf("soapCalls=%d getCalls=%d, want 1 and 1 (SOAP update then canonical REST read)", soapCalls, getCalls)
	}
	var got customTargetingValueResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if got.DisplayName.ValueString() != "Honda Updated" {
		t.Errorf("display_name = %q, want Honda Updated", got.DisplayName.ValueString())
	}
}

// TestCustomTargetingValueUpdateNoopCarriesComputed guards the no-write branch
// (only skip_archive_on_destroy changed): the computed status, Unknown in the
// plan, must be carried forward from prior state and no API call is made.
func TestCustomTargetingValueUpdateNoopCarriesComputed(t *testing.T) {
	ctx := context.Background()
	sch := valueTestSchema(t)
	state := customTargetingValueAPIToModel(apiCustomTargetingValue(), types.BoolValue(false))
	plan := state
	plan.SkipArchiveOnDestroy = types.BoolValue(true)
	plan.Status = types.StringUnknown()

	r := &customTargetingValueResource{} // no clients: the no-op branch must not call the API
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: sch}}
	r.Update(ctx, resource.UpdateRequest{Plan: newValuePlan(t, plan), State: newValueState(t, state)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", resp.Diagnostics)
	}
	var got customTargetingValueResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatalf("reading state: %v", d)
	}
	if !got.SkipArchiveOnDestroy.ValueBool() {
		t.Error("skip_archive_on_destroy should update to true")
	}
	if got.Status.IsUnknown() || got.Status.ValueString() != "ACTIVE" {
		t.Errorf("computed status not carried from state: %q", got.Status.ValueString())
	}
}

// --- Delete ------------------------------------------------------------------

func TestCustomTargetingValueDeleteDeactivates(t *testing.T) {
	ctx := context.Background()
	var soapCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isSOAPRequest(r):
			soapCalls++
			_, _ = w.Write([]byte(soapActionResponse))
		case r.Method == http.MethodGet:
			getCalls++
			t.Error("a successful SOAP delete must not trigger a REST re-read")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := newValueTestResource(t, srv)
	m := customTargetingValueAPIToModel(apiCustomTargetingValue(), types.BoolValue(false))
	resp := &resource.DeleteResponse{State: newValueState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newValueState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete: %v", resp.Diagnostics)
	}
	if soapCalls != 1 || getCalls != 0 {
		t.Errorf("soapCalls=%d getCalls=%d, want 1 and 0", soapCalls, getCalls)
	}
}

// TestCustomTargetingValueDeleteToleratesAlreadyInactive: a SOAP fault is
// tolerated only when the value actually reads back INACTIVE over REST.
func TestCustomTargetingValueDeleteToleratesAlreadyInactive(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isSOAPRequest(r):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(soapFaultResponse))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(restValueJSON("INACTIVE")))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := newValueTestResource(t, srv)
	m := customTargetingValueAPIToModel(apiCustomTargetingValue(), types.BoolValue(false))
	resp := &resource.DeleteResponse{State: newValueState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newValueState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("already-inactive value should be tolerated: %v", resp.Diagnostics)
	}
	if len(resp.Diagnostics.Warnings()) == 0 {
		t.Error("expected a warning noting the value was already inactive")
	}
}

// TestCustomTargetingValueDeleteSurfacesRealFailure: a SOAP fault while the value
// still reads back ACTIVE must surface an error, not silently drop a live value.
func TestCustomTargetingValueDeleteSurfacesRealFailure(t *testing.T) {
	ctx := context.Background()
	var soapCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isSOAPRequest(r):
			soapCalls++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(soapFaultResponse))
		case r.Method == http.MethodGet:
			getCalls++
			_, _ = w.Write([]byte(restValueJSON("ACTIVE")))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := newValueTestResource(t, srv)
	m := customTargetingValueAPIToModel(apiCustomTargetingValue(), types.BoolValue(false))
	resp := &resource.DeleteResponse{State: newValueState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newValueState(t, m)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatalf("expected an error when the SOAP delete fails and the value is still ACTIVE; diags = %v", resp.Diagnostics)
	}
	if soapCalls != 1 || getCalls != 1 {
		t.Errorf("soapCalls=%d getCalls=%d, want 1 and 1 (re-verify via REST before tolerating)", soapCalls, getCalls)
	}
}

func TestCustomTargetingValueDeleteSkipTouchesNothing(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("skip_archive_on_destroy must not call the API; got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	r := newValueTestResource(t, srv)
	m := customTargetingValueAPIToModel(apiCustomTargetingValue(), types.BoolValue(true))
	resp := &resource.DeleteResponse{State: newValueState(t, m)}
	r.Delete(ctx, resource.DeleteRequest{State: newValueState(t, m)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("skip delete should not error: %v", resp.Diagnostics)
	}
}

// --- Import ------------------------------------------------------------------

func TestCustomTargetingValueImportState(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	r := newValueTestResource(t, srv)

	t.Run("bare numeric id", func(t *testing.T) {
		// The framework hands ImportState an all-null typed object; mirror that so
		// SetAttribute has a typed object to write into.
		resp := &resource.ImportStateResponse{State: newValueState(t, customTargetingValueResourceModel{})}
		r.ImportState(ctx, resource.ImportStateRequest{ID: "555"}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("import: %v", resp.Diagnostics)
		}
		var got customTargetingValueResourceModel
		if d := resp.State.Get(ctx, &got); d.HasError() {
			t.Fatalf("reading state: %v", d)
		}
		if got.ID.ValueString() != "networks/123456/customTargetingValues/555" {
			t.Errorf("imported id = %q", got.ID.ValueString())
		}
	})

	t.Run("invalid id", func(t *testing.T) {
		resp := &resource.ImportStateResponse{State: newValueState(t, customTargetingValueResourceModel{})}
		r.ImportState(ctx, resource.ImportStateRequest{ID: "customTargetingValues/555"}, resp)
		if !resp.Diagnostics.HasError() {
			t.Error("expected an error for a malformed import id")
		}
	})
}
