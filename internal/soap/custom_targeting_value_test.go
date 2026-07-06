package soap

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createResponseXML is a canned createCustomTargetingValues response echoing a
// server-assigned id. Shared with soap_test.go's rate-limiter test.
const createResponseXML = `<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Header><ResponseHeader xmlns="https://www.google.com/apis/ads/publisher/v202605"><requestId>abc</requestId></ResponseHeader></soap:Header>
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

// capturingServer records the last request body and serves a fixed response.
func capturingServer(t *testing.T, body *string, response string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*body = string(raw)
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(response))
	}))
}

// assertOrder fails unless each needle appears, in the given order, in haystack.
func assertOrder(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	prev := -1
	for _, n := range needles {
		idx := strings.Index(haystack, n)
		if idx < 0 {
			t.Errorf("missing element %q in payload", n)
			return
		}
		if idx <= prev {
			t.Errorf("element %q is out of WSDL sequence order in payload", n)
		}
		prev = idx
	}
}

func TestCreateCustomTargetingValueEnvelope(t *testing.T) {
	var reqBody string
	srv := capturingServer(t, &reqBody, createResponseXML)
	defer srv.Close()

	c := newTestClient(t, srv, "")
	got, err := c.CreateCustomTargetingValue(context.Background(), Value{
		CustomTargetingKeyID: 321,
		Name:                 "honda",
		DisplayName:          "Honda",
		MatchType:            "EXACT",
	})
	if err != nil {
		t.Fatalf("CreateCustomTargetingValue: %v", err)
	}

	// Header: networkCode then applicationName, in the versioned namespace.
	var env struct {
		NetworkCode     string `xml:"Header>RequestHeader>networkCode"`
		ApplicationName string `xml:"Header>RequestHeader>applicationName"`
		Op              struct {
			XMLName              xml.Name
			CustomTargetingKeyID string `xml:"values>customTargetingKeyId"`
			ID                   string `xml:"values>id"`
			Name                 string `xml:"values>name"`
			DisplayName          string `xml:"values>displayName"`
			MatchType            string `xml:"values>matchType"`
			Status               string `xml:"values>status"`
		} `xml:"Body>createCustomTargetingValues"`
	}
	if err := xml.Unmarshal([]byte(reqBody), &env); err != nil {
		t.Fatalf("parsing request envelope: %v\n%s", err, reqBody)
	}
	if env.NetworkCode != "123456" {
		t.Errorf("networkCode = %q, want 123456", env.NetworkCode)
	}
	if env.ApplicationName != "terraform-provider-admanager/test" {
		t.Errorf("applicationName = %q", env.ApplicationName)
	}
	if env.Op.XMLName.Local != "createCustomTargetingValues" {
		t.Errorf("operation element = %q, want createCustomTargetingValues", env.Op.XMLName.Local)
	}
	if env.Op.XMLName.Space != apiNamespace {
		t.Errorf("operation namespace = %q, want %q", env.Op.XMLName.Space, apiNamespace)
	}
	if env.Op.CustomTargetingKeyID != "321" || env.Op.Name != "honda" ||
		env.Op.DisplayName != "Honda" || env.Op.MatchType != "EXACT" {
		t.Errorf("value payload fields = %+v", env.Op)
	}
	// Read-only fields must never be sent on create.
	if env.Op.ID != "" {
		t.Errorf("create payload must not carry the read-only id, got %q", env.Op.ID)
	}
	if env.Op.Status != "" {
		t.Errorf("create payload must not carry the read-only status, got %q", env.Op.Status)
	}
	// Element order inside <values> must follow the WSDL sequence.
	assertOrder(t, reqBody, "<customTargetingKeyId>", "<name>", "<displayName>", "<matchType>")

	// Response bridging: the created object comes back with its assigned id.
	if got.ID != 555 || got.CustomTargetingKeyID != 321 || got.Name != "honda" ||
		got.DisplayName != "Honda" || got.MatchType != "EXACT" || got.Status != "ACTIVE" {
		t.Errorf("decoded created value = %+v", got)
	}
}

func TestUpdateCustomTargetingValueEnvelope(t *testing.T) {
	const updateResp = `<?xml version="1.0"?>
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

	var reqBody string
	srv := capturingServer(t, &reqBody, updateResp)
	defer srv.Close()

	c := newTestClient(t, srv, "")
	got, err := c.UpdateCustomTargetingValue(context.Background(), Value{
		CustomTargetingKeyID: 321,
		ID:                   555,
		Name:                 "honda",
		DisplayName:          "Honda Updated",
		MatchType:            "EXACT",
	})
	if err != nil {
		t.Fatalf("UpdateCustomTargetingValue: %v", err)
	}

	var env struct {
		Op struct {
			XMLName xml.Name
			ID      string `xml:"values>id"`
			Name    string `xml:"values>name"`
			Status  string `xml:"values>status"`
		} `xml:"Body>updateCustomTargetingValues"`
	}
	if err := xml.Unmarshal([]byte(reqBody), &env); err != nil {
		t.Fatalf("parsing update envelope: %v", err)
	}
	if env.Op.XMLName.Local != "updateCustomTargetingValues" {
		t.Errorf("operation = %q, want updateCustomTargetingValues", env.Op.XMLName.Local)
	}
	// Update REPLACES the object, so the id must be present.
	if env.Op.ID != "555" {
		t.Errorf("update payload must carry the id, got %q", env.Op.ID)
	}
	// The read-only status must still never be sent.
	if env.Op.Status != "" {
		t.Errorf("update payload must not carry read-only status, got %q", env.Op.Status)
	}
	// Full-object write order still holds.
	assertOrder(t, reqBody, "<customTargetingKeyId>", "<id>", "<name>", "<displayName>", "<matchType>")

	if got.DisplayName != "Honda Updated" {
		t.Errorf("decoded displayName = %q", got.DisplayName)
	}
}

func TestUpdateCustomTargetingValueRequiresID(t *testing.T) {
	c := newTestClient(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), "")
	if _, err := c.UpdateCustomTargetingValue(context.Background(), Value{CustomTargetingKeyID: 1, Name: "x"}); err == nil {
		t.Fatal("expected an error when the value id is zero")
	}
}

func TestDeleteCustomTargetingValueEnvelope(t *testing.T) {
	const actionResp = `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <performCustomTargetingValueActionResponse xmlns="https://www.google.com/apis/ads/publisher/v202605">
      <rval><numChanges>1</numChanges></rval>
    </performCustomTargetingValueActionResponse>
  </soap:Body>
</soap:Envelope>`

	var reqBody string
	srv := capturingServer(t, &reqBody, actionResp)
	defer srv.Close()

	c := newTestClient(t, srv, "")
	n, err := c.DeleteCustomTargetingValue(context.Background(), 321, 555)
	if err != nil {
		t.Fatalf("DeleteCustomTargetingValue: %v", err)
	}
	if n != 1 {
		t.Errorf("numChanges = %d, want 1", n)
	}

	var env struct {
		Op struct {
			XMLName xml.Name
			Action  struct {
				XsiType string `xml:"type,attr"`
			} `xml:"customTargetingValueAction"`
			Query  string `xml:"filterStatement>query"`
			Values []struct {
				Key   string `xml:"key"`
				Value struct {
					XsiType string `xml:"type,attr"`
					Value   string `xml:"value"`
				} `xml:"value"`
			} `xml:"filterStatement>values"`
		} `xml:"Body>performCustomTargetingValueAction"`
	}
	if err := xml.Unmarshal([]byte(reqBody), &env); err != nil {
		t.Fatalf("parsing action envelope: %v\n%s", err, reqBody)
	}
	if env.Op.XMLName.Local != "performCustomTargetingValueAction" {
		t.Errorf("operation = %q", env.Op.XMLName.Local)
	}
	if env.Op.Action.XsiType != "tns:DeleteCustomTargetingValues" {
		t.Errorf("action xsi:type = %q, want tns:DeleteCustomTargetingValues", env.Op.Action.XsiType)
	}
	// PQL must use bind variables, never interpolate the ids into the query.
	if env.Op.Query != "WHERE customTargetingKeyId = :keyId AND id = :valueId" {
		t.Errorf("query = %q", env.Op.Query)
	}
	if strings.Contains(env.Op.Query, "321") || strings.Contains(env.Op.Query, "555") {
		t.Errorf("PQL injection risk: ids interpolated into query %q", env.Op.Query)
	}
	// Bind values carry the ids as typed NumberValue entries.
	if len(env.Op.Values) != 2 {
		t.Fatalf("bind values = %+v, want 2 (keyId, valueId)", env.Op.Values)
	}
	binds := map[string]string{}
	for _, v := range env.Op.Values {
		if v.Value.XsiType != "tns:NumberValue" {
			t.Errorf("bind %q value xsi:type = %q, want tns:NumberValue", v.Key, v.Value.XsiType)
		}
		binds[v.Key] = v.Value.Value
	}
	if binds["keyId"] != "321" || binds["valueId"] != "555" {
		t.Errorf("bind values = %v, want keyId=321 valueId=555", binds)
	}
}

func TestKeyIDFromResourceName(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"networks/123456/customTargetingKeys/321", 321, false},
		{"321", 321, false},
		{"", 0, true},
		{"networks/123456/customTargetingKeys/abc", 0, true},
	}
	for _, tc := range cases {
		got, err := KeyIDFromResourceName(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("KeyIDFromResourceName(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("KeyIDFromResourceName(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
}

func TestValueIDFromResourceName(t *testing.T) {
	got, err := ValueIDFromResourceName("networks/123456/customTargetingValues/555")
	if err != nil || got != 555 {
		t.Errorf("ValueIDFromResourceName = %d, %v; want 555", got, err)
	}
	if _, err := ValueIDFromResourceName("networks/123456/customTargetingValues/nope"); err == nil {
		t.Error("expected error for a non-numeric id")
	}
}

func TestValueResourceName(t *testing.T) {
	c := NewClient(Config{NetworkCode: "123456"})
	if got := c.ValueResourceName(555); got != "networks/123456/customTargetingValues/555" {
		t.Errorf("ValueResourceName = %q", got)
	}
}
