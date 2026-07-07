package soap

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// emptyRValResponse builds a well-formed 200 SOAP response for op whose <rval>
// list is empty, exercising the "returned no value" guards.
func emptyRValResponse(op string) string {
	return `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <` + op + `Response xmlns="https://www.google.com/apis/ads/publisher/v202605"></` + op + `Response>
  </soap:Body>
</soap:Envelope>`
}

func TestCreateCustomTargetingValueEmptyRValErrors(t *testing.T) {
	// A 200 with no <rval> must not be silently accepted; the guard
	// (custom_targeting_value.go:55-57) turns it into an error rather than
	// indexing into an empty slice.
	var body string
	srv := capturingServer(t, &body, emptyRValResponse("createCustomTargetingValues"))
	defer srv.Close()

	c := newTestClient(t, srv, "")
	if _, err := c.CreateCustomTargetingValue(context.Background(),
		Value{CustomTargetingKeyID: 1, Name: "x", MatchType: "EXACT"}); err == nil {
		t.Fatal("expected an error when the service returns no value, got nil")
	}
}

func TestUpdateCustomTargetingValueEmptyRValErrors(t *testing.T) {
	var body string
	srv := capturingServer(t, &body, emptyRValResponse("updateCustomTargetingValues"))
	defer srv.Close()

	c := newTestClient(t, srv, "")
	if _, err := c.UpdateCustomTargetingValue(context.Background(),
		Value{CustomTargetingKeyID: 1, ID: 555, Name: "x", MatchType: "EXACT"}); err == nil {
		t.Fatal("expected an error when the service returns no value, got nil")
	}
}

// performActionCountResponse builds a 200 performCustomTargetingValueAction
// response reporting the given numChanges.
func performActionCountResponse(numChanges int) string {
	return `<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body><performCustomTargetingValueActionResponse xmlns="https://www.google.com/apis/ads/publisher/v202605">
    <rval><numChanges>` + strconv.Itoa(numChanges) + `</numChanges></rval>
  </performCustomTargetingValueActionResponse></soap:Body>
</soap:Envelope>`
}

// TestDeleteCustomTargetingValueNoIDGuard documents the CURRENT behavior: unlike
// UpdateCustomTargetingValue (which rejects a zero id), DeleteCustomTargetingValue
// has NO id guard — it serializes whatever ids it is handed straight into the PQL
// bind values and issues the request. Callers pass ids already validated by
// KeyIDFromResourceName/ValueIDFromResourceName, so this is not currently a bug,
// but the asymmetry with Update is intentional to lock in. The server returns
// numChanges=1 here so the change-count guard (below) does not fire; this test is
// only about how the ids reach the wire, not the response.
func TestDeleteCustomTargetingValueNoIDGuard(t *testing.T) {
	var body string
	srv := capturingServer(t, &body, performActionCountResponse(1))
	defer srv.Close()

	c := newTestClient(t, srv, "")
	// Zero and negative ids are NOT rejected: the call reaches the server.
	if _, err := c.DeleteCustomTargetingValue(context.Background(), 0, 0); err != nil {
		t.Fatalf("DeleteCustomTargetingValue(0,0): unexpected error %v", err)
	}
	if !strings.Contains(body, "<value>0</value>") {
		t.Errorf("expected zero ids serialized into the bind values, body:\n%s", body)
	}

	body = ""
	if _, err := c.DeleteCustomTargetingValue(context.Background(), -7, -9); err != nil {
		t.Fatalf("DeleteCustomTargetingValue(-7,-9): unexpected error %v", err)
	}
	if !strings.Contains(body, "<value>-7</value>") || !strings.Contains(body, "<value>-9</value>") {
		t.Errorf("expected negative ids serialized verbatim, body:\n%s", body)
	}
}

// TestDeleteCustomTargetingValueChangeCountGuard mirrors UpdateCustomTargetingValue's
// identity guard on the delete path: the action MUST report exactly one change.
// numChanges=1 is the only success. numChanges=0 means the PQL statement matched
// nothing (the value was not deactivated, so the caller must not treat it as
// success); numChanges>1 would mean the statement swept in more than the single
// intended value. Both are surfaced as errors naming the key/value ids and the
// observed count.
func TestDeleteCustomTargetingValueChangeCountGuard(t *testing.T) {
	cases := []struct {
		name       string
		numChanges int
		wantErr    bool
	}{
		{"zero changes is not success", 0, true},
		{"exactly one change succeeds", 1, false},
		{"two changes is over-broad", 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body string
			srv := capturingServer(t, &body, performActionCountResponse(tc.numChanges))
			defer srv.Close()

			c := newTestClient(t, srv, "")
			n, err := c.DeleteCustomTargetingValue(context.Background(), 321, 555)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("numChanges=%d: expected an error, got nil (n=%d)", tc.numChanges, n)
				}
				// The error must name the ids and the observed count so an operator
				// can locate the offending statement.
				for _, want := range []string{"321", "555", strconv.Itoa(tc.numChanges)} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("numChanges=%d: unexpected error %v", tc.numChanges, err)
			}
			if n != 1 {
				t.Errorf("numChanges = %d, want 1", n)
			}
		})
	}
}

// TestNumericIDAcceptsNegative pins the known quirk: strconv.ParseInt accepts a
// leading '-', so a resource name whose trailing segment is a negative integer
// parses without error. Asserted as current behavior, not endorsed.
func TestNumericIDAcceptsNegative(t *testing.T) {
	got, err := KeyIDFromResourceName("networks/123456/customTargetingKeys/-5")
	if err != nil || got != -5 {
		t.Errorf("KeyIDFromResourceName(.../-5) = %d, %v; want -5, nil (negative-id quirk)", got, err)
	}
	got, err = ValueIDFromResourceName("-42")
	if err != nil || got != -42 {
		t.Errorf("ValueIDFromResourceName(-42) = %d, %v; want -42, nil", got, err)
	}
}

// idOracle mirrors numericIDFromName's extraction to predict both the numeric id
// AND whether it should error for a given name, giving the fuzz targets a
// faithful reference. Returning the value (not just wantErr) lets the fuzzers
// pin the extracted int64, catching value-corruption bugs in the trailing-segment
// slicing that leave the error decision unchanged.
func idOracle(name string) (int64, bool) { // returns id, wantErr
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return 0, true
	}
	idPart := trimmed
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		idPart = trimmed[i+1:]
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	return id, err != nil
}

// FuzzKeyIDFromResourceName drives the two-arg numericIDFromName parser through
// its public key extractor with arbitrary resource names. It must never panic,
// and its error decision must match the base-10 parse of the trailing segment.
func FuzzKeyIDFromResourceName(f *testing.F) {
	for _, seed := range []string{
		"networks/123456/customTargetingKeys/321", "321", "", "  ",
		"networks/123456/customTargetingKeys/abc", "networks/1/customTargetingKeys/-5",
		"networks/1/customTargetingKeys/89", "/", "///", "networks//",
		"99999999999999999999999", "007",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		id, err := KeyIDFromResourceName(name)
		wantID, wantErr := idOracle(name)
		if (err != nil) != wantErr {
			t.Fatalf("KeyIDFromResourceName(%q) err=%v, want error=%v", name, err, wantErr)
		}
		if err == nil && id != wantID {
			t.Fatalf("KeyIDFromResourceName(%q) = %d, want %d", name, id, wantID)
		}
	})
}

// FuzzValueIDFromResourceName is the same contract for the value extractor.
func FuzzValueIDFromResourceName(f *testing.F) {
	for _, seed := range []string{
		"networks/123456/customTargetingValues/555", "555", "", "nope",
		"networks/1/customTargetingValues/-42", "networks/1/customTargetingValues/0x10",
		"networks/1/customTargetingValues/89", "98",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		id, err := ValueIDFromResourceName(name)
		wantID, wantErr := idOracle(name)
		if (err != nil) != wantErr {
			t.Fatalf("ValueIDFromResourceName(%q) err=%v, want error=%v", name, err, wantErr)
		}
		if err == nil && id != wantID {
			t.Fatalf("ValueIDFromResourceName(%q) = %d, want %d", name, id, wantID)
		}
	})
}

// FuzzValueEnvelopeRoundTrip proves that arbitrary value strings — including the
// XML metacharacters &<>" and control runes — are safely escaped by
// marshalEnvelope and cannot break out of the WSDL-ordered envelope structure.
// The round-tripped envelope must still parse and still carry the fixed header
// and operation element; for XML-safe inputs the value must survive intact.
func FuzzValueEnvelopeRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"honda", `a&b<c>d"e`, "</values><injected>", "&amp;", "\x00\x01\x02",
		"line1\nline2", strings.Repeat("x", 300), "<?xml?>", "",
	} {
		f.Add(seed)
	}
	c := NewClient(Config{NetworkCode: "123456", ApplicationName: "app"})
	f.Fuzz(func(t *testing.T, valueName string) {
		// Feed DISTINCT strings into Name and DisplayName (and decode both) so the
		// round-trip catches a Name/DisplayName field-identity or WSDL-order mixup
		// — with identical values a swapped struct tag would go unnoticed.
		displayName := "display:" + valueName
		req := &createRequest{Xmlns: apiNamespace, Values: []Value{{
			CustomTargetingKeyID: 321, Name: valueName, DisplayName: displayName, MatchType: "EXACT",
		}}}
		data, err := c.marshalEnvelope(req)
		if err != nil {
			t.Fatalf("marshalEnvelope(%q) error: %v", valueName, err)
		}

		var decoded struct {
			NetworkCode string `xml:"Header>RequestHeader>networkCode"`
			Op          struct {
				XMLName     xml.Name
				Name        string `xml:"values>name"`
				DisplayName string `xml:"values>displayName"`
			} `xml:"Body>createCustomTargetingValues"`
		}
		if err := xml.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("round-trip unmarshal failed for %q — value broke out of the envelope: %v\n%s", valueName, err, data)
		}
		// Structure must be intact regardless of the payload content.
		if decoded.NetworkCode != "123456" {
			t.Fatalf("header networkCode corrupted to %q by payload %q", decoded.NetworkCode, valueName)
		}
		if decoded.Op.XMLName.Local != "createCustomTargetingValues" {
			t.Fatalf("operation element corrupted to %q by payload %q", decoded.Op.XMLName.Local, valueName)
		}
		// For payloads with no control runes, encoding/xml round-trips the text
		// exactly (metacharacters are escaped, not dropped). Assert BOTH fields so a
		// tag swap that misroutes Name into <displayName> (or vice versa) is caught.
		if isXMLTextSafe(valueName) {
			if decoded.Op.Name != valueName {
				t.Fatalf("name not round-tripped: got %q, want %q", decoded.Op.Name, valueName)
			}
			if decoded.Op.DisplayName != displayName {
				t.Fatalf("displayName not round-tripped: got %q, want %q", decoded.Op.DisplayName, displayName)
			}
		}
	})
}

// isXMLTextSafe reports whether s contains only runes encoding/xml preserves
// verbatim in element text (valid, non-control code points). Control runes are
// replaced by U+FFFD, so identity round-trip is not expected for them.
func isXMLTextSafe(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}
