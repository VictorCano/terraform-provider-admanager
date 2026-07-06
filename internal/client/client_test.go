package client

// All tests run against local httptest servers with a static fake token.
// No real Google credentials are ever used or required here.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

// testClient builds a Client pointed at the given test server, with retry
// backoff shrunk to keep the suite fast.
func testClient(t *testing.T, srv *httptest.Server, cfg Config) *Client {
	t.Helper()
	cfg.BaseURL = srv.URL
	if cfg.NetworkCode == "" {
		cfg.NetworkCode = "123456"
	}
	if cfg.RequestsPerSecond == 0 {
		// Keep the limiter out of the way unless a test opts in to a slow one.
		cfg.RequestsPerSecond = 200
	}
	if cfg.TokenSource == nil {
		cfg.TokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"})
	}
	c, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.retryBaseDelay = time.Millisecond
	return c
}

func TestNewRequiresNetworkCode(t *testing.T) {
	_, err := New(context.Background(), Config{
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t"}),
	})
	if err == nil {
		t.Fatal("expected error for missing network code, got nil")
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	c, err := New(context.Background(), Config{
		NetworkCode: "123456",
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t"}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.limiter.Limit(); got != rate.Limit(2) {
		t.Errorf("default rate limit = %v, want 2", got)
	}
	if c.maxAttempts != 5 {
		t.Errorf("default max attempts = %d, want 5", c.maxAttempts)
	}
	if c.baseURL != "https://admanager.googleapis.com" {
		t.Errorf("default base URL = %q", c.baseURL)
	}
}

func TestGetNetworkDecodesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/networks/123456" {
			t.Errorf("path = %q, want /v1/networks/123456", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		_, _ = fmt.Fprint(w, `{
			"name": "networks/123456",
			"networkCode": "123456",
			"displayName": "Test Network",
			"timeZone": "America/Sao_Paulo",
			"currencyCode": "BRL",
			"secondaryCurrencyCodes": ["USD"],
			"effectiveRootAdUnit": "networks/123456/adUnits/1",
			"networkId": "9999",
			"testNetwork": true
		}`)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	n, err := c.GetNetwork(context.Background())
	if err != nil {
		t.Fatalf("GetNetwork: %v", err)
	}
	if n.NetworkCode != "123456" || n.DisplayName != "Test Network" ||
		n.TimeZone != "America/Sao_Paulo" || n.CurrencyCode != "BRL" ||
		n.EffectiveRootAdUnit != "networks/123456/adUnits/1" ||
		n.NetworkID != "9999" || !n.TestNetwork {
		t.Errorf("unexpected network decoded: %+v", n)
	}
}

func TestRateLimiterSpacesSequentialRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"networks/123456","networkCode":"123456"}`)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{RequestsPerSecond: 50})
	start := time.Now()
	for i := 0; i < 11; i++ {
		if _, err := c.GetNetwork(context.Background()); err != nil {
			t.Fatalf("GetNetwork #%d: %v", i, err)
		}
	}
	// 11 requests at 50 req/s with burst 1 must take at least 10*20ms = 200ms.
	// Assert against 150ms to leave room for coarse timers.
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("11 requests took only %v; rate limiter not applied", elapsed)
	}
}

func TestGetRetriesOn503ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"name":"networks/123456","networkCode":"123456"}`)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	if _, err := c.GetNetwork(context.Background()); err != nil {
		t.Fatalf("GetNetwork: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server saw %d calls, want 3", got)
	}
}

func TestGetStopsAfterMaxAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{RetryMaxAttempts: 3})
	_, err := c.GetNetwork(context.Background())
	if err == nil {
		t.Fatal("expected error after exhausting attempts, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("error = %v, want APIError with status 503", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server saw %d calls, want exactly 3 (attempts include the first call)", got)
	}
}

func TestRetryAfterHeaderIsHonored(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = fmt.Fprint(w, `{"name":"networks/123456","networkCode":"123456"}`)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	start := time.Now()
	if _, err := c.GetNetwork(context.Background()); err != nil {
		t.Fatalf("GetNetwork: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("retry happened after %v; Retry-After: 1 not honored", elapsed)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server saw %d calls, want 2", got)
	}
}

func TestWritesAreNotRetriedOn5xx(t *testing.T) {
	// A 5xx on a write may mean the server already processed the request;
	// retrying could create the entity twice. Writes must fail fast on 5xx.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	err := c.do(context.Background(), http.MethodPost, "/v1/networks/123456/adUnits", nil,
		map[string]string{"displayName": "x"}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d calls, want 1 (no retry of writes on 5xx)", got)
	}
}

func TestWritesAreRetriedOn429(t *testing.T) {
	// A 429 means the request was rejected before processing, so retrying a
	// write cannot double-apply it.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = fmt.Fprint(w, `{"name":"networks/123456/adUnits/1"}`)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{})
	var out struct {
		Name string `json:"name"`
	}
	err := c.do(context.Background(), http.MethodPost, "/v1/networks/123456/adUnits", nil,
		map[string]string{"displayName": "x"}, &out)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if out.Name != "networks/123456/adUnits/1" {
		t.Errorf("decoded name = %q", out.Name)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server saw %d calls, want 2", got)
	}
}

func TestErrorsDoNotContainCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "super-secret-token-value"}),
	})
	_, err := c.GetNetwork(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "super-secret-token-value") {
		t.Fatalf("error message leaks the access token: %q", msg)
	}
	if !strings.Contains(msg, "PERMISSION_DENIED") || !strings.Contains(msg, "does not have permission") {
		t.Errorf("error message missing actionable API context: %q", msg)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("error = %v, want APIError with status 403", err)
	}
}

func TestResolveCredentialsJSON(t *testing.T) {
	inline := `{"type":"service_account","project_id":"p"}`
	got, err := resolveCredentialsJSON(inline)
	if err != nil || string(got) != inline {
		t.Errorf("inline JSON: got %q, err %v", got, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "sa.json")
	if err := os.WriteFile(path, []byte(inline), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveCredentialsJSON(path)
	if err != nil || string(got) != inline {
		t.Errorf("file path: got %q, err %v", got, err)
	}

	got, err = resolveCredentialsJSON("")
	if err != nil || got != nil {
		t.Errorf("empty: got %q, err %v; want nil, nil (use ADC)", got, err)
	}

	if _, err = resolveCredentialsJSON(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file: expected error, got nil")
	}
}

func TestResolveCredentialsNeverLeaksContentInErrors(t *testing.T) {
	// A UTF-8 BOM (not stripped by TrimSpace) or any stray leading character
	// makes inline JSON fail the "{" prefix check. The value must NEVER fall
	// through to a path lookup whose *os.PathError would echo the whole
	// credential — private key included — into a Terraform diagnostic.
	blob := "\ufeff" + `{"type":"service_account","private_key":"PRIVATE-KEY-MATERIAL"}`
	data, err := resolveCredentialsJSON(blob)
	if err == nil {
		// Even better: BOM-prefixed JSON is accepted as inline content.
		if !strings.Contains(string(data), "service_account") {
			t.Fatalf("BOM-prefixed JSON neither errored nor parsed: %q", data)
		}
	} else if strings.Contains(err.Error(), "PRIVATE-KEY-MATERIAL") {
		t.Fatalf("error leaks credential material: %v", err)
	}

	// A blob that is clearly not a path (braces, newlines, huge) but also not
	// inline JSON must produce an error that does not echo the value.
	garbled := "x" + strings.Repeat("A", 100) + `"private_key":"PRIVATE-KEY-MATERIAL"` + "\n{}"
	_, err = resolveCredentialsJSON(garbled)
	if err == nil {
		t.Fatal("expected error for garbled credentials, got nil")
	}
	if strings.Contains(err.Error(), "PRIVATE-KEY-MATERIAL") {
		t.Fatalf("error leaks credential material: %v", err)
	}

	// Plain missing file: fine to fail, but the error must not carry key
	// material either (defense in depth: we never echo the raw value).
	_, err = resolveCredentialsJSON("/nonexistent/path/sa.json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d, ok := parseRetryAfter("3"); !ok || d != 3*time.Second {
		t.Errorf(`parseRetryAfter("3") = %v, %v; want 3s, true`, d, ok)
	}
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	if d, ok := parseRetryAfter(future); !ok || d <= 0 || d > 3*time.Second {
		t.Errorf("parseRetryAfter(http date) = %v, %v; want ~2s, true", d, ok)
	}
	if _, ok := parseRetryAfter("garbage"); ok {
		t.Error(`parseRetryAfter("garbage") ok = true, want false`)
	}
	if _, ok := parseRetryAfter(""); ok {
		t.Error(`parseRetryAfter("") ok = true, want false`)
	}
}
