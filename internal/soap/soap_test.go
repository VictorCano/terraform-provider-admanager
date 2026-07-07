package soap

// All tests run against local httptest servers with a static fake token. No real
// Google credentials are ever used or required here.

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

// newTestClient builds a Client whose HTTP client carries a static bearer token
// and whose limiter is effectively unlimited, pointed at srv.
func newTestClient(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	if token == "" {
		token = "test-token"
	}
	httpClient := oauth2.NewClient(context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	return NewClient(Config{
		HTTPClient:      httpClient,
		Limiter:         rate.NewLimiter(rate.Inf, 1),
		NetworkCode:     "123456",
		ApplicationName: "terraform-provider-admanager/test",
		BaseURL:         srv.URL,
	})
}

// faultResponse is a representative ApiException SOAP fault (HTTP 500).
const faultResponse = `<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <soap:Body>
    <soap:Fault>
      <faultcode>soap:Server</faultcode>
      <faultstring>[NotNullError.NULL_VALUE @ values[0].name]</faultstring>
      <detail>
        <ApiExceptionFault xmlns="https://www.google.com/apis/ads/publisher/v202605" xsi:type="ApiException">
          <message>[NotNullError.NULL_VALUE @ values[0].name]</message>
          <errors xsi:type="NotNullError">
            <fieldPath>values[0].name</fieldPath>
            <trigger></trigger>
            <errorString>NotNullError.NULL_VALUE</errorString>
            <reason>NULL_VALUE</reason>
          </errors>
        </ApiExceptionFault>
      </detail>
    </soap:Fault>
  </soap:Body>
</soap:Envelope>`

func TestSOAPFaultParsedIntoTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(faultResponse))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "")
	_, err := c.CreateCustomTargetingValue(context.Background(), Value{
		CustomTargetingKeyID: 321, MatchType: "EXACT",
	})
	if err == nil {
		t.Fatal("expected a SOAP fault error, got nil")
	}
	var se *SOAPError
	if !errors.As(err, &se) {
		t.Fatalf("error is not *SOAPError: %T (%v)", err, err)
	}
	if se.HTTPStatus != http.StatusInternalServerError {
		t.Errorf("HTTPStatus = %d, want 500", se.HTTPStatus)
	}
	if len(se.Errors) != 1 {
		t.Fatalf("Errors = %+v, want exactly one", se.Errors)
	}
	e := se.Errors[0]
	if e.ErrorString != "NotNullError.NULL_VALUE" || e.Type != "NotNullError" ||
		e.Reason != "NULL_VALUE" || e.FieldPath != "values[0].name" {
		t.Errorf("parsed error = %+v", e)
	}
	// The actionable code must reach the error string.
	if !strings.Contains(err.Error(), "NotNullError.NULL_VALUE") {
		t.Errorf("Error() = %q, want it to mention the error code", err.Error())
	}
}

// TestSOAPErrorNeverLeaksToken is the credential-leakage guard: no error path may
// surface the bearer token, whether the failure is a parsed fault or a transport
// error.
func TestSOAPErrorNeverLeaksToken(t *testing.T) {
	const token = "super-secret-oauth-token-value-42"

	t.Run("fault", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(faultResponse))
		}))
		defer srv.Close()

		c := newTestClient(t, srv, token)
		_, err := c.CreateCustomTargetingValue(context.Background(), Value{CustomTargetingKeyID: 1, Name: "x", MatchType: "EXACT"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("error string leaked the token: %q", err.Error())
		}
	})

	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		srv.Close() // force a connection failure

		c := newTestClient(t, srv, token)
		_, err := c.CreateCustomTargetingValue(context.Background(), Value{CustomTargetingKeyID: 1, Name: "x", MatchType: "EXACT"})
		if err == nil {
			t.Fatal("expected a transport error")
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("transport error leaked the token: %q", err.Error())
		}
	})
}

// TestSOAPNonFaultErrorSurfacesStatus checks the fallback path: a non-SOAP body
// (e.g. an OAuth 401) becomes a status-bearing error without leaking the token.
func TestSOAPNonFaultErrorSurfacesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`Request had invalid authentication credentials.`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "secret")
	_, err := c.CreateCustomTargetingValue(context.Background(), Value{CustomTargetingKeyID: 1, Name: "x", MatchType: "EXACT"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("Error() = %q, want it to surface HTTP 401", err.Error())
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaked token: %q", err.Error())
	}
}

// TestSOAPCallWaitsOnRateLimiter proves every SOAP call goes through the shared
// token bucket: with a bucket that grants one token then refills only hourly, the
// second call blocks on the limiter and — under a short context deadline — fails
// there, so the server never receives it.
func TestSOAPCallWaitsOnRateLimiter(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(createResponseXML))
	}))
	defer srv.Close()

	httpClient := oauth2.NewClient(context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t"}))
	c := NewClient(Config{
		HTTPClient:      httpClient,
		Limiter:         rate.NewLimiter(rate.Every(time.Hour), 1), // 1 token, then ~hourly
		NetworkCode:     "123456",
		ApplicationName: "app",
		BaseURL:         srv.URL,
	})

	if _, err := c.CreateCustomTargetingValue(context.Background(), Value{CustomTargetingKeyID: 1, Name: "x", MatchType: "EXACT"}); err != nil {
		t.Fatalf("first call: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.CreateCustomTargetingValue(ctx, Value{CustomTargetingKeyID: 1, Name: "y", MatchType: "EXACT"})
	if err == nil {
		t.Fatal("second call should have been blocked by the rate limiter")
	}
	if requests != 1 {
		t.Errorf("server saw %d requests, want 1 (the limiter must gate the second call)", requests)
	}
}

// TestAPIErrorCode exercises every arm of APIError.code(), the fallback ladder
// that picks the most specific identifier available for one fault error.
func TestAPIErrorCode(t *testing.T) {
	cases := []struct {
		name string
		in   APIError
		want string
	}{
		{"errorString wins", APIError{ErrorString: "AuthError.BAD", Type: "AuthError", Reason: "BAD"}, "AuthError.BAD"},
		{"type and reason", APIError{Type: "AuthError", Reason: "BAD"}, "AuthError.BAD"},
		{"reason only", APIError{Reason: "BAD"}, "BAD"},
		{"type only", APIError{Type: "AuthError"}, "AuthError"},
		{"all empty", APIError{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.code(); got != tc.want {
				t.Errorf("code() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseFaultNonFaultSnippetTruncated proves the fallback snippet cap
// (soap.go:339): a large non-SOAP error body is trimmed to at most 512 bytes so
// a hostile response cannot bloat the surfaced error.
func TestParseFaultNonFaultSnippetTruncated(t *testing.T) {
	raw := []byte(strings.Repeat("Z", 2000))
	err := parseFault(http.StatusInternalServerError, raw)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if n := strings.Count(msg, "Z"); n != 512 {
		t.Errorf("snippet carried %d body bytes, want exactly 512 (the cap)", n)
	}
}

// faultOracle independently decides whether raw is a recognizable SOAP fault,
// mirroring parseFault's detection condition. It gives FuzzParseFault a faithful
// reference so the fuzzer can assert the RETURN TYPE, not just non-nilness: a
// body carrying a faultstring or structured errors must become a *SOAPError, and
// any regression that misroutes a real fault into the generic status-only
// fallback (dropping Type/Reason/ErrorString/FieldPath) fails the fuzz target.
func faultOracle(raw []byte) bool {
	var fe faultEnvelope
	if err := xml.Unmarshal(raw, &fe); err != nil {
		return false
	}
	return fe.Fault.FaultString != "" || len(fe.Fault.Detail.APIException.Errors) > 0
}

// FuzzParseFault throws arbitrary bytes at parseFault (the SOAP response body is
// an untrusted-input boundary). It must never panic and must always return a
// non-nil error for a non-2xx status. When the body is a recognizable SOAP
// fault, the error must be a *SOAPError carrying the parsed detail.
func FuzzParseFault(f *testing.F) {
	for _, seed := range []string{
		faultResponse, "", "   ", "<not-xml", "<soap:Envelope></soap:Envelope>",
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><soap:Fault><faultstring>boom</faultstring></soap:Fault></soap:Body></soap:Envelope>`,
		"Request had invalid authentication credentials.", strings.Repeat("A", 3000),
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		err := parseFault(http.StatusInternalServerError, raw)
		if err == nil {
			t.Fatalf("parseFault returned nil error for a non-2xx status; body=%q", raw)
		}
		if faultOracle(raw) {
			var se *SOAPError
			if !errors.As(err, &se) {
				t.Fatalf("parseFault(%q) = %T (%v); want *SOAPError for a recognizable fault", raw, err, err)
			}
		}
	})
}
