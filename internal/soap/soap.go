// Package soap is a minimal, hand-built compatibility shim for the one Ad
// Manager operation the REST API cannot do: writing custom targeting *values*.
//
// # Why this exists
//
// The Ad Manager REST API (admanager.googleapis.com, v1) exposes custom
// targeting values as read-only — networks.customTargetingValues has get and
// list, but no create/update/delete (verified against the discovery doc, rev
// 20260701). The provider still needs to manage values, so writes go through the
// legacy SOAP CustomTargetingService while reads stay on REST. See CLAUDE.md
// "SOAP shim for custom targeting values". The Terraform schema never leaks SOAP
// details, so the shim can be deleted the moment REST ships value writes.
//
// Design constraints (mirrored from internal/client)
//
//   - Shared infrastructure: the shim is constructed with the REST client's own
//     oauth2-authenticated *http.Client and its *rate.Limiter. Nothing here
//     builds a parallel HTTP client or token bucket; every call waits on the
//     shared limiter before touching the network, so REST and SOAP draw from one
//     rate budget.
//   - Single-attempt writes: all three operations are writes/actions. A SOAP 5xx
//     or fault is ambiguous about whether the server already applied the change,
//     so — exactly like the REST client's non-GET policy — writes are never
//     retried. A retry could create a duplicate value or reapply a mutation.
//   - No credential leakage: errors carry only what the SOAP *response* returned
//     (faultstring and the ApiException error codes). The request envelope and
//     the Authorization header are never embedded in an error.
//
// The XML is hand-built with encoding/xml (no third-party SOAP library). Request
// payload structs are ordered to match the WSDL sequences exactly, because
// encoding/xml emits struct fields in declaration order and Ad Manager's
// document/literal binding is order-sensitive.
package soap

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/time/rate"
)

// soapVersion pins the Ad Manager SOAP API version that backs custom targeting
// value writes. This is the SINGLE place to bump it.
//
// SOAP versions sunset roughly 12 months after release on a rolling quarterly
// schedule (https://developers.google.com/ad-manager/api/deprecation); v202605
// sunsets around May 2027. A stale version breaks value writes for every user,
// so per CLAUDE.md's version policy this must be advanced at least twice a year
// (target the newest available version at bump time).
const soapVersion = "v202605"

const (
	// apiNamespace is the versioned Ad Manager SOAP namespace. Header and body
	// payload elements live here.
	apiNamespace = "https://www.google.com/apis/ads/publisher/" + soapVersion

	// defaultBaseURL is the SOAP service host. The full endpoint appends the
	// version and service name.
	defaultBaseURL = "https://ads.google.com/apis/ads/publisher"

	soapEnvNS = "http://schemas.xmlsoap.org/soap/envelope/"
	xsiNS     = "http://www.w3.org/2001/XMLSchema-instance"

	// maxResponseBytes caps how much of a response body is read. Value operations
	// return tiny payloads (a single object or a change count); this is generous.
	maxResponseBytes = 4 << 20
)

// Config carries everything the shim needs. HTTPClient and Limiter come straight
// from the REST *client.Client (its HTTPClient() and Limiter() accessors) so the
// shim shares that client's credentials and rate budget.
type Config struct {
	// HTTPClient is the oauth2-authenticated client shared with the REST side.
	HTTPClient *http.Client

	// Limiter is the shared token bucket. Every call waits on it.
	Limiter *rate.Limiter

	// NetworkCode scopes every request (sent in the SOAP RequestHeader).
	NetworkCode string

	// ApplicationName is sent as RequestHeader.applicationName; the provider
	// passes "terraform-provider-admanager/<version>".
	ApplicationName string

	// BaseURL overrides the SOAP host. Used by tests; empty means production.
	BaseURL string
}

// Client issues SOAP calls to CustomTargetingService.
type Client struct {
	httpClient      *http.Client
	limiter         *rate.Limiter
	networkCode     string
	applicationName string
	endpoint        string
}

// NewClient builds a Client from cfg. The endpoint is derived from the pinned
// soapVersion so callers never spell the version out.
func NewClient(cfg Config) *Client {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	endpoint := strings.TrimRight(base, "/") + "/" + soapVersion + "/CustomTargetingService"
	return &Client{
		httpClient:      cfg.HTTPClient,
		limiter:         cfg.Limiter,
		networkCode:     cfg.NetworkCode,
		applicationName: cfg.ApplicationName,
		endpoint:        endpoint,
	}
}

// --- Envelope (request marshalling) -----------------------------------------

type soapEnvelope struct {
	XMLName xml.Name `xml:"soap:Envelope"`
	SoapNS  string   `xml:"xmlns:soap,attr"`
	XsiNS   string   `xml:"xmlns:xsi,attr"`
	// TnsNS binds the "tns" prefix to the versioned namespace at envelope scope so
	// xsi:type values (which are QNames) can reference concrete API types
	// unambiguously, e.g. xsi:type="tns:NumberValue". An unprefixed QName in an
	// attribute value has NO namespace per the XML namespaces spec, so the prefix
	// is required for a strict server to resolve the type against the schema.
	TnsNS  string     `xml:"xmlns:tns,attr"`
	Header soapHeader `xml:"soap:Header"`
	Body   soapBody   `xml:"soap:Body"`
}

// tnsType qualifies a concrete API type name with the tns prefix for use in an
// xsi:type attribute.
func tnsType(local string) string { return "tns:" + local }

type soapHeader struct {
	RequestHeader requestHeader `xml:"RequestHeader"`
}

// requestHeader is the SoapRequestHeader: networkCode then applicationName, in
// that WSDL-defined order, in the versioned namespace (emitted as the default
// namespace of the element).
type requestHeader struct {
	Xmlns           string `xml:"xmlns,attr"`
	NetworkCode     string `xml:"networkCode"`
	ApplicationName string `xml:"applicationName"`
}

// soapBody wraps the operation payload. Content's own XMLName supplies the
// element name and namespace.
type soapBody struct {
	Content any
}

// marshalEnvelope wraps a payload in a full SOAP 1.1 envelope with the request
// header. The payload struct is responsible for its element name and namespace.
func (c *Client) marshalEnvelope(payload any) ([]byte, error) {
	env := soapEnvelope{
		SoapNS: soapEnvNS,
		XsiNS:  xsiNS,
		TnsNS:  apiNamespace,
		Header: soapHeader{RequestHeader: requestHeader{
			Xmlns:           apiNamespace,
			NetworkCode:     c.networkCode,
			ApplicationName: c.applicationName,
		}},
		Body: soapBody{Content: payload},
	}
	out, err := xml.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("soap: encoding request envelope: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// call performs one SOAP operation: waits on the shared limiter, POSTs the
// envelope built from payload, and decodes a 2xx response into out. It is a
// single attempt — writes are never retried (see the package comment). A non-2xx
// response is parsed into a *SOAPError when it carries an ApiException fault.
func (c *Client) call(ctx context.Context, payload, out any) error {
	body, err := c.marshalEnvelope(payload)
	if err != nil {
		return err
	}

	// Wait on the SHARED token bucket before every attempt; nothing bypasses it.
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("soap: waiting for rate limiter: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("soap: building request: %w", err)
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	// SOAP 1.1 requires a SOAPAction header; CustomTargetingService uses an empty
	// action (soapAction="" in the WSDL binding).
	req.Header.Set("SOAPAction", "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Transport-level failure. It is unknowable whether a write reached the
		// server, and writes are never retried, so surface it. The wrapped error
		// carries the endpoint URL but never the request body or token.
		return fmt.Errorf("soap: calling CustomTargetingService: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseFault(resp.StatusCode, raw)
	}
	if readErr != nil {
		return fmt.Errorf("soap: reading response: %w", readErr)
	}
	if out != nil {
		if err := xml.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("soap: decoding response: %w", err)
		}
	}
	return nil
}

// --- Faults ------------------------------------------------------------------

// SOAPError is a SOAP fault returned by CustomTargetingService. It carries only
// data from the response — never the request envelope, headers, or token.
type SOAPError struct {
	// HTTPStatus is the HTTP status the fault arrived with (typically 500).
	HTTPStatus int
	// FaultString is the SOAP faultstring.
	FaultString string
	// Errors are the structured ApiException errors, when present.
	Errors []APIError
}

// APIError is one entry from an ApiException's error list.
type APIError struct {
	// Type is the concrete error class from xsi:type, e.g. "AuthenticationError".
	Type string
	// Reason is the enum reason, e.g. "GOOGLE_ACCOUNT_ALREADY_ASSOCIATED_WITH_NETWORK".
	Reason string
	// ErrorString is the API's combined code, e.g.
	// "AuthenticationError.GOOGLE_ACCOUNT_ALREADY_ASSOCIATED_WITH_NETWORK".
	ErrorString string
	// FieldPath points at the offending field, when the API supplies one.
	FieldPath string
}

// code renders the most specific identifier available for one error.
func (e APIError) code() string {
	switch {
	case e.ErrorString != "":
		return e.ErrorString
	case e.Type != "" && e.Reason != "":
		return e.Type + "." + e.Reason
	case e.Reason != "":
		return e.Reason
	default:
		return e.Type
	}
}

func (e *SOAPError) Error() string {
	if len(e.Errors) > 0 {
		parts := make([]string, 0, len(e.Errors))
		for _, er := range e.Errors {
			c := er.code()
			if er.FieldPath != "" {
				c += " (field " + er.FieldPath + ")"
			}
			if c != "" {
				parts = append(parts, c)
			}
		}
		if len(parts) > 0 {
			return "Ad Manager SOAP fault: " + strings.Join(parts, "; ")
		}
	}
	if e.FaultString != "" {
		return "Ad Manager SOAP fault: " + e.FaultString
	}
	return fmt.Sprintf("Ad Manager SOAP request failed with HTTP %d", e.HTTPStatus)
}

// faultEnvelope decodes the SOAP 1.1 fault shape. The fault detail carries an
// ApiExceptionFault element (WSDL: message ApiException -> element
// tns:ApiExceptionFault, type ApiException). Fields are matched by local name so
// namespaces do not need to be spelled out.
type faultEnvelope struct {
	Fault struct {
		FaultCode   string `xml:"faultcode"`
		FaultString string `xml:"faultstring"`
		Detail      struct {
			APIException struct {
				Message string        `xml:"message"`
				Errors  []apiErrorXML `xml:"errors"`
			} `xml:"ApiExceptionFault"`
		} `xml:"detail"`
	} `xml:"Body>Fault"`
}

type apiErrorXML struct {
	// Type is the xsi:type attribute (local name "type", any namespace).
	Type        string `xml:"type,attr"`
	FieldPath   string `xml:"fieldPath"`
	Trigger     string `xml:"trigger"`
	ErrorString string `xml:"errorString"`
	Reason      string `xml:"reason"`
}

// parseFault turns a non-2xx response into a *SOAPError. When the body is not a
// recognizable SOAP fault, it falls back to a status-only error with a bounded,
// token-free snippet of the response body.
func parseFault(status int, raw []byte) error {
	var fe faultEnvelope
	if err := xml.Unmarshal(raw, &fe); err == nil &&
		(fe.Fault.FaultString != "" || len(fe.Fault.Detail.APIException.Errors) > 0) {
		se := &SOAPError{HTTPStatus: status, FaultString: fe.Fault.FaultString}
		for _, e := range fe.Fault.Detail.APIException.Errors {
			se.Errors = append(se.Errors, APIError{
				Type:        e.Type,
				Reason:      e.Reason,
				ErrorString: e.ErrorString,
				FieldPath:   e.FieldPath,
			})
		}
		return se
	}
	// Not a SOAP fault (e.g. an OAuth 401 with a plain body). Surface the status
	// with a trimmed snippet; the response body never contains our token.
	snippet := strings.TrimSpace(string(raw))
	if len(snippet) > 512 {
		snippet = snippet[:512]
	}
	if snippet == "" {
		return fmt.Errorf("soap: CustomTargetingService returned HTTP %d", status)
	}
	return fmt.Errorf("soap: CustomTargetingService returned HTTP %d: %s", status, snippet)
}
