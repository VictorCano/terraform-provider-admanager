// Package client implements a minimal REST client for the Google Ad Manager
// API (admanager.googleapis.com, v1).
//
// The Ad Manager API has no official Go client library (verified 2026-07-05),
// so this package hand-rolls the small surface the provider needs. Two design
// requirements from the project spec are enforced here for every call:
//
//   - Rate limiting: Ad Manager quotas are in the single-digit requests per
//     second, so every request goes through a token bucket sized from the
//     provider configuration. Nothing in this package may bypass it.
//   - Careful retries: 429 responses are retried for every HTTP method,
//     because a 429 means the request was rejected before being processed.
//     5xx responses and transport errors are retried only for GET requests;
//     after an ambiguous failure a write may already have been applied, and
//     retrying it could create or mutate an entity twice.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/oauth2adapt"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL = "https://admanager.googleapis.com"

	// scopeAdManager grants read/write access to the Ad Manager REST API. The API
	// also offers an admanager.readonly scope, but the provider needs to mutate
	// entities.
	scopeAdManager = "https://www.googleapis.com/auth/admanager"

	// scopeDFP is the legacy DoubleClick for Publishers scope of the SOAP API,
	// which backs custom targeting *value* writes (the REST API has no value
	// write endpoints; discovery doc rev 20260701). Empirically (2026-07-06)
	// Google aliases this scope to the admanager scope at consent time and the
	// SOAP API accepts admanager-scoped tokens, so scopeDFP may be redundant —
	// it is still requested defensively in case the aliasing is ever undone.
	// The SOAP shim reuses this client's HTTP client and token source. See
	// CLAUDE.md "SOAP shim for custom targeting values".
	scopeDFP = "https://www.googleapis.com/auth/dfp"

	defaultRequestsPerSecond = 2
	defaultMaxAttempts       = 5
	defaultRetryBaseDelay    = 500 * time.Millisecond
	maxRetryDelay            = 16 * time.Second

	// defaultMaxListPages caps how many pages a paginated list call will fetch
	// before erroring. It exists so a too-broad (or missing) filter cannot spin
	// an unbounded loop of API calls; the error tells the caller to narrow the
	// filter rather than silently truncating the results.
	defaultMaxListPages = 100

	// maxErrorBodyBytes caps how much of an error response is read into
	// error messages.
	maxErrorBodyBytes = 4 << 10
)

// Config carries everything needed to build a Client. It mirrors the
// provider-level configuration block.
type Config struct {
	// NetworkCode is the Ad Manager network code all requests are scoped to.
	NetworkCode string

	// Credentials is either a path to a service account JSON key file or the
	// JSON content itself. Empty means Application Default Credentials.
	Credentials string

	// RequestsPerSecond sizes the client-side token bucket. Zero or negative
	// falls back to a conservative default of 2.
	RequestsPerSecond float64

	// RetryMaxAttempts is the maximum number of attempts per API call,
	// including the first one. Zero or negative falls back to 5.
	RetryMaxAttempts int

	// BaseURL overrides the API endpoint. Used by tests.
	BaseURL string

	// TokenSource overrides credential resolution entirely. Used by tests.
	TokenSource oauth2.TokenSource

	// UserAgent is sent with every request.
	UserAgent string
}

// Client is a rate-limited, retrying HTTP client for the Ad Manager API.
// All API calls must go through do so that the rate limiter and the retry
// policy apply uniformly.
type Client struct {
	httpClient     *http.Client
	baseURL        string
	networkCode    string
	userAgent      string
	limiter        *rate.Limiter
	maxAttempts    int
	retryBaseDelay time.Duration
	maxListPages   int
}

// New builds a Client from cfg, resolving credentials in this order: explicit
// TokenSource (tests), service account JSON (path or inline content), then
// Application Default Credentials.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.NetworkCode == "" {
		return nil, errors.New("admanager client: network code is required")
	}

	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = defaultRequestsPerSecond
	}
	attempts := cfg.RetryMaxAttempts
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = "terraform-provider-admanager"
	}

	ts := cfg.TokenSource
	if ts == nil {
		data, err := resolveCredentialsJSON(cfg.Credentials)
		if err != nil {
			return nil, err
		}
		// DetectDefault uses the provided service account JSON when set and
		// falls back to Application Default Credentials when data is nil.
		creds, err := credentials.DetectDefault(&credentials.DetectOptions{
			Scopes:          []string{scopeAdManager, scopeDFP},
			CredentialsJSON: data,
		})
		if err != nil {
			return nil, fmt.Errorf("admanager client: resolving Google credentials: %w", err)
		}
		ts = oauth2adapt.TokenSourceFromTokenProvider(creds)
	}

	return &Client{
		httpClient:  oauth2.NewClient(ctx, ts),
		baseURL:     strings.TrimRight(baseURL, "/"),
		networkCode: cfg.NetworkCode,
		userAgent:   userAgent,
		// Burst of 1 keeps requests evenly spaced instead of allowing an
		// initial spike, which is what low per-second quotas require.
		limiter:        rate.NewLimiter(rate.Limit(rps), 1),
		maxAttempts:    attempts,
		retryBaseDelay: defaultRetryBaseDelay,
		maxListPages:   defaultMaxListPages,
	}, nil
}

// maxCredentialsPathLen is a generous upper bound for a real filesystem
// path; anything longer is treated as (malformed) inline content.
const maxCredentialsPathLen = 4096

// resolveCredentialsJSON turns the provider-level credentials setting into
// raw JSON bytes: inline JSON is used as-is, anything else is treated as a
// file path. Empty means "use Application Default Credentials" (nil, nil).
//
// SECURITY: no error returned from here may ever embed the raw credentials
// value. A value that was meant to be inline JSON but fails the checks below
// would otherwise reach os.ReadFile, whose *os.PathError echoes the entire
// "path" — private key included — and Configure would print it verbatim in a
// Terraform diagnostic.
func resolveCredentialsJSON(credentials string) ([]byte, error) {
	// A UTF-8 BOM is not whitespace, so TrimSpace alone would leave it in
	// front of the "{" and misroute inline JSON to the file-path branch.
	// Windows tooling (e.g. PowerShell Out-File) commonly emits BOMs.
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(credentials), "\ufeff"))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		return []byte(trimmed), nil
	}
	// Values that cannot plausibly be a path are malformed inline content;
	// reject them without echoing the value anywhere.
	if strings.ContainsAny(trimmed, "{}\"\n") || len(trimmed) > maxCredentialsPathLen {
		return nil, errors.New(
			"admanager client: the credentials value looks like inline JSON but does not start with '{'; " +
				"remove any leading BOM or stray characters (the value is not shown to avoid leaking key material)")
	}
	data, err := os.ReadFile(trimmed) //nolint:gosec // G304: the path is the operator's own credentials setting.
	if err != nil {
		// Do not wrap err: *os.PathError embeds the full configured value,
		// which must never reach a diagnostic. Keep only the cause.
		reason := "unreadable"
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && pathErr.Err != nil {
			reason = pathErr.Err.Error()
		}
		return nil, fmt.Errorf("admanager client: reading the credentials file at the configured path: %s", reason)
	}
	return data, nil
}

// APIError is a non-2xx response from the Ad Manager API. It carries only
// what the API itself returned — never request headers or credentials.
type APIError struct {
	StatusCode int
	// Status is the canonical RPC status, e.g. "PERMISSION_DENIED".
	Status  string
	Message string
}

func (e *APIError) Error() string {
	if e.Status != "" {
		return fmt.Sprintf("Ad Manager API error %d (%s): %s", e.StatusCode, e.Status, e.Message)
	}
	return fmt.Sprintf("Ad Manager API error %d: %s", e.StatusCode, e.Message)
}

// do performs one logical API call: waits on the rate limiter, sends the
// request, applies the retry policy, and decodes a JSON response into out
// when out is non-nil. body, when non-nil, is JSON-encoded once and replayed
// on every retry.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("admanager client: encoding request body: %w", err)
		}
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	// Only GETs are safe to replay after an ambiguous failure.
	idempotent := method == http.MethodGet

	for attempt := 1; ; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return fmt.Errorf("admanager client: waiting for rate limiter: %w", err)
		}

		var reqBody io.Reader
		if payload != nil {
			reqBody = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
		if err != nil {
			return fmt.Errorf("admanager client: building request: %w", err)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Transport-level failure: it is unknowable whether the request
			// reached the server, so only GETs are retried.
			transportErr := fmt.Errorf("admanager client: calling Ad Manager API: %w", err)
			if !idempotent || attempt >= c.maxAttempts {
				return transportErr
			}
			if err := sleepCtx(ctx, c.backoff(attempt)); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out != nil {
				err = json.NewDecoder(resp.Body).Decode(out)
			} else {
				_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
			}
			_ = resp.Body.Close()
			if err != nil {
				return fmt.Errorf("admanager client: decoding Ad Manager API response: %w", err)
			}
			return nil
		}

		apiErr := parseAPIError(resp)

		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			(idempotent && isRetryable5xx(resp.StatusCode))
		if !retryable || attempt >= c.maxAttempts {
			return apiErr
		}

		delay := c.backoff(attempt)
		if ra, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			delay = ra
		}
		if err := sleepCtx(ctx, delay); err != nil {
			return err
		}
	}
}

func isRetryable5xx(status int) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// errorDetail is one entry of the google.rpc error `details` array. A single
// struct decodes every shape the provider surfaces (ErrorInfo, BadRequest,
// LocalizedMessage); the `@type` suffix selects which fields are meaningful.
// Only response-body content is decoded — never headers or credentials — and
// ErrorInfo `metadata` (consumer/project identifiers) is deliberately omitted.
type errorDetail struct {
	Type            string `json:"@type"`
	Reason          string `json:"reason"`  // ErrorInfo
	Domain          string `json:"domain"`  // ErrorInfo
	Message         string `json:"message"` // LocalizedMessage
	FieldViolations []struct {
		Field       string `json:"field"`
		Description string `json:"description"`
	} `json:"fieldViolations"` // BadRequest
}

// parseAPIError converts a non-2xx response into an *APIError, reading at
// most maxErrorBodyBytes of the body and always closing it.
func parseAPIError(resp *http.Response) *APIError {
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))

	apiErr := &APIError{StatusCode: resp.StatusCode}
	var envelope struct {
		Error struct {
			Message string        `json:"message"`
			Status  string        `json:"status"`
			Details []errorDetail `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil &&
		(envelope.Error.Message != "" || envelope.Error.Status != "" || len(envelope.Error.Details) > 0) {
		apiErr.Status = envelope.Error.Status
		apiErr.Message = envelope.Error.Message
		// The Ad Manager API frequently returns an opaque top-level message
		// ("An error occurred. Please try again later.") while the actionable
		// cause lives in the google.rpc `details` array. Fold a compact,
		// credential-safe summary of those details into the message so
		// diagnostics name the real reason / offending fields.
		if summary := summarizeErrorDetails(envelope.Error.Details, envelope.Error.Message); summary != "" {
			if apiErr.Message == "" {
				apiErr.Message = summary
			} else {
				apiErr.Message += "; " + summary
			}
		}
		return apiErr
	}
	apiErr.Message = strings.TrimSpace(string(body))
	return apiErr
}

// summarizeErrorDetails renders a compact "; "-joined human summary of the
// google.rpc details: ErrorInfo -> "reason: X" / "domain: Y",
// BadRequest.fieldViolations -> "field: description", LocalizedMessage -> its
// message (skipped when it merely echoes topMessage). It carries only
// API-returned content — no headers, tokens, or ErrorInfo metadata.
func summarizeErrorDetails(details []errorDetail, topMessage string) string {
	var parts []string
	for _, d := range details {
		switch {
		case strings.HasSuffix(d.Type, "ErrorInfo"):
			if d.Reason != "" {
				parts = append(parts, "reason: "+d.Reason)
			}
			if d.Domain != "" {
				parts = append(parts, "domain: "+d.Domain)
			}
		case strings.HasSuffix(d.Type, "BadRequest"):
			for _, fv := range d.FieldViolations {
				if fv.Field == "" && fv.Description == "" {
					continue
				}
				parts = append(parts, strings.TrimSpace(fv.Field+": "+fv.Description))
			}
		case strings.HasSuffix(d.Type, "LocalizedMessage"):
			if d.Message != "" && d.Message != topMessage {
				parts = append(parts, d.Message)
			}
		}
	}
	return strings.Join(parts, "; ")
}

// backoff returns the delay before the retry that follows the given 1-based
// attempt: exponential growth with equal jitter (a random value in
// [d/2, d)), so concurrent clients do not retry in lockstep.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.retryBaseDelay * (1 << (attempt - 1))
	if d <= 0 || d > maxRetryDelay {
		d = maxRetryDelay
	}
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half) //nolint:gosec // G404: retry jitter is not security-sensitive.
}

// parseRetryAfter understands both forms of the Retry-After header: a number
// of seconds and an HTTP date. Any honored delay is CLAMPED to maxRetryDelay:
// the header expresses the server's intent to have us wait, but the client's
// own retry ceiling bounds how long that wait may be. Clamping (rather than
// rejecting) keeps huge values non-hammering without blocking for centuries.
func parseRetryAfter(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		// The seconds->Duration multiplication can overflow int64 (roughly 292
		// years or more), wrapping to a negative Duration. Detect that and clamp;
		// a representable-but-huge value clamps for the same bounded-wait reason.
		if int64(secs) > math.MaxInt64/int64(time.Second) {
			return maxRetryDelay, true
		}
		return clampRetryDelay(time.Duration(secs) * time.Second), true
	}
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0, true
		}
		return clampRetryDelay(d), true
	}
	return 0, false
}

// clampRetryDelay bounds a positive Retry-After delay to the client's retry
// ceiling, so no server value can hold a call open longer than maxRetryDelay.
func clampRetryDelay(d time.Duration) time.Duration {
	if d > maxRetryDelay {
		return maxRetryDelay
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
