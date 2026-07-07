package client

// All tests run against local httptest servers or synthesized responses with a
// static fake token. No real Google credentials are ever used or required here.

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestParseAPIErrorNonJSONBody exercises the fallback at client.go:345-377: a
// plain-text / HTML body that is not the JSON error envelope must become the
// APIError message verbatim (trimmed), with an empty Status.
func TestParseAPIErrorNonJSONBody(t *testing.T) {
	const body = "  <html><body>502 Bad Gateway</body></html>  "
	apiErr := parseAPIError(responseFrom(http.StatusBadGateway, body))

	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	if apiErr.Status != "" {
		t.Errorf("Status = %q, want empty (non-JSON body has no canonical status)", apiErr.Status)
	}
	if apiErr.Message != strings.TrimSpace(body) {
		t.Errorf("Message = %q, want the trimmed raw body %q", apiErr.Message, strings.TrimSpace(body))
	}
}

// TestParseAPIErrorTruncatesLargeBody proves the maxErrorBodyBytes cap
// (client.go:70): a non-JSON body larger than the cap is read only up to the
// cap, so the surfaced message never grows unbounded with a hostile response.
func TestParseAPIErrorTruncatesLargeBody(t *testing.T) {
	body := strings.Repeat("A", maxErrorBodyBytes+5000)
	apiErr := parseAPIError(responseFrom(http.StatusBadGateway, body))

	if len(apiErr.Message) != maxErrorBodyBytes {
		t.Errorf("message length = %d, want exactly maxErrorBodyBytes (%d)", len(apiErr.Message), maxErrorBodyBytes)
	}
}

// responseFrom builds a minimal *http.Response carrying body, as parseAPIError
// consumes it (it reads and closes Body and reads StatusCode only).
func responseFrom(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestParseAPIErrorServiceDisabledDetails replays the real 403 SERVICE_DISABLED
// payload (google.rpc.ErrorInfo) the Ad Manager REST API returns when the API is
// not enabled on the caller's quota project (see dev/test-network.md). The bare
// top-level message is opaque; the ErrorInfo.reason must be surfaced.
func TestParseAPIErrorServiceDisabledDetails(t *testing.T) {
	payload := `{
	  "error": {
	    "code": 403,
	    "message": "Ad Manager API has not been used in project 123456789012 before or it is disabled.",
	    "status": "PERMISSION_DENIED",
	    "details": [
	      {
	        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
	        "reason": "SERVICE_DISABLED",
	        "domain": "googleapis.com",
	        "metadata": {
	          "consumer": "projects/123456789012",
	          "service": "admanager.googleapis.com"
	        }
	      },
	      {
	        "@type": "type.googleapis.com/google.rpc.LocalizedMessage",
	        "locale": "en-US",
	        "message": "Ad Manager API has not been used in project 123456789012 before or it is disabled."
	      }
	    ]
	  }
	}`
	apiErr := parseAPIError(responseFrom(http.StatusForbidden, payload))

	if apiErr.StatusCode != http.StatusForbidden || apiErr.Status != "PERMISSION_DENIED" {
		t.Fatalf("status = %d (%q), want 403 PERMISSION_DENIED", apiErr.StatusCode, apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "reason: SERVICE_DISABLED") {
		t.Errorf("message %q does not surface the ErrorInfo reason", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, "domain: googleapis.com") {
		t.Errorf("message %q does not surface the ErrorInfo domain", apiErr.Message)
	}
	// The original top-level message must still be present.
	if !strings.Contains(apiErr.Message, "has not been used in project") {
		t.Errorf("message %q dropped the top-level message", apiErr.Message)
	}
	// Dedup: the LocalizedMessage echoes the top-level message verbatim, so it
	// must NOT be appended a second time (the top-level phrase appears once).
	if n := strings.Count(apiErr.Message, "Ad Manager API has not been used in project"); n != 1 {
		t.Errorf("top-level message appears %d times, want 1 (echoing LocalizedMessage must be deduped): %q", n, apiErr.Message)
	}
	// Metadata (consumer/service) is intentionally not appended (avoid leaking
	// consumer/project identifiers into diagnostics).
	if strings.Contains(apiErr.Message, "projects/123456789012") {
		t.Errorf("message %q leaks ErrorInfo metadata", apiErr.Message)
	}
}

// TestParseAPIErrorFieldViolations surfaces google.rpc.BadRequest.fieldViolations
// (field + description) so a 400 names exactly which fields are wrong instead of
// "An error occurred. Please try again later."
func TestParseAPIErrorFieldViolations(t *testing.T) {
	payload := `{
	  "error": {
	    "code": 400,
	    "message": "Request contains an invalid argument.",
	    "status": "INVALID_ARGUMENT",
	    "details": [
	      {
	        "@type": "type.googleapis.com/google.rpc.BadRequest",
	        "fieldViolations": [
	          {"field": "adUnit.displayName", "description": "must not be empty"},
	          {"field": "adUnit.adUnitCode", "description": "is already in use"}
	        ]
	      }
	    ]
	  }
	}`
	apiErr := parseAPIError(responseFrom(http.StatusBadRequest, payload))

	if apiErr.Status != "INVALID_ARGUMENT" {
		t.Fatalf("status = %q, want INVALID_ARGUMENT", apiErr.Status)
	}
	for _, want := range []string{
		"adUnit.displayName: must not be empty",
		"adUnit.adUnitCode: is already in use",
	} {
		if !strings.Contains(apiErr.Message, want) {
			t.Errorf("message %q missing field violation %q", apiErr.Message, want)
		}
	}
}

// TestParseAPIErrorLocalizedMessageExpansion covers the additive branch of the
// LocalizedMessage handling in summarizeErrorDetails: a common Google API shape
// where the top-level message is generic while google.rpc.LocalizedMessage
// carries a locale-specific expansion that differs from it. The expansion must
// be surfaced in the diagnostic (not skipped as a dedup echo) and must appear
// exactly once. The sibling TestParseAPIErrorServiceDisabledDetails covers the
// opposite (dedup/skip) branch where LocalizedMessage merely echoes the top-level
// message; together they exercise both arms of the LocalizedMessage case.
func TestParseAPIErrorLocalizedMessageExpansion(t *testing.T) {
	const expansion = `The ad_unit_code "czmb_widescreen_footer" is already in use by an archived ad unit; unarchive it or choose a different code.`
	payload := `{
	  "error": {
	    "code": 400,
	    "message": "Request contains an invalid argument.",
	    "status": "INVALID_ARGUMENT",
	    "details": [
	      {
	        "@type": "type.googleapis.com/google.rpc.LocalizedMessage",
	        "locale": "en-US",
	        "message": "The ad_unit_code \"czmb_widescreen_footer\" is already in use by an archived ad unit; unarchive it or choose a different code."
	      }
	    ]
	  }
	}`
	apiErr := parseAPIError(responseFrom(http.StatusBadRequest, payload))

	if apiErr.Status != "INVALID_ARGUMENT" {
		t.Fatalf("status = %q, want INVALID_ARGUMENT", apiErr.Status)
	}
	// The additive LocalizedMessage expansion must be surfaced, not dropped.
	if !strings.Contains(apiErr.Message, expansion) {
		t.Errorf("message %q does not surface the additive LocalizedMessage expansion", apiErr.Message)
	}
	// The generic top-level message must still lead the diagnostic.
	if !strings.Contains(apiErr.Message, "Request contains an invalid argument.") {
		t.Errorf("message %q dropped the top-level message", apiErr.Message)
	}
	// The expansion must appear exactly once — no duplication.
	if n := strings.Count(apiErr.Message, expansion); n != 1 {
		t.Errorf("LocalizedMessage expansion appears %d times, want exactly 1: %q", n, apiErr.Message)
	}
}

// TestParseAPIErrorNoDetailsUnchanged is the regression guard: a payload without
// a details array must leave the message exactly as the top-level message, with
// no trailing separators or empty summary appended.
func TestParseAPIErrorNoDetailsUnchanged(t *testing.T) {
	payload := `{"error":{"code":404,"message":"AdUnit not found","status":"NOT_FOUND"}}`
	apiErr := parseAPIError(responseFrom(http.StatusNotFound, payload))

	if apiErr.Status != "NOT_FOUND" {
		t.Fatalf("status = %q, want NOT_FOUND", apiErr.Status)
	}
	if apiErr.Message != "AdUnit not found" {
		t.Errorf("message = %q, want exactly \"AdUnit not found\" (unchanged when no details)", apiErr.Message)
	}
}
