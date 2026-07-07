package client

// All tests run against local httptest servers with a static fake token.
// No real Google credentials are ever used or required here.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// countingErrTransport is a RoundTripper that fails the first failN calls with a
// transport-level error (nil response, non-nil error) and then serves a fixed
// 200 body. failN < 0 fails every call. It lets the retry tests exercise the
// transport-error branch (client.go:273-285) deterministically, without relying
// on a killed listener whose attempts cannot be counted.
type countingErrTransport struct {
	calls int32
	failN int32
	body  string
}

func (t *countingErrTransport) RoundTrip(*http.Request) (*http.Response, error) {
	n := atomic.AddInt32(&t.calls, 1)
	if t.failN < 0 || n <= t.failN {
		return nil, fmt.Errorf("simulated transport failure")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Header:     make(http.Header),
	}, nil
}

func TestGetRetriesOnTransportError(t *testing.T) {
	// A transport error on a GET is ambiguous but safe to replay (the request is
	// idempotent), so the client retries per client.go:273-285. Fail twice, then
	// succeed on the third attempt.
	tr := &countingErrTransport{failN: 2, body: `{"name":"networks/123456","networkCode":"123456"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c := testClient(t, srv, Config{})
	c.httpClient = &http.Client{Transport: tr}

	if _, err := c.GetNetwork(context.Background()); err != nil {
		t.Fatalf("GetNetwork: %v", err)
	}
	if got := atomic.LoadInt32(&tr.calls); got != 3 {
		t.Errorf("transport saw %d attempts, want 3 (two failures then success)", got)
	}
}

func TestWritesNotRetriedOnTransportError(t *testing.T) {
	// A transport error on a write may mean the server already applied it, so
	// writes must NOT be replayed (locked double-write protection). Exactly one
	// attempt regardless of maxAttempts.
	tr := &countingErrTransport{failN: -1}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c := testClient(t, srv, Config{})
	c.httpClient = &http.Client{Transport: tr}

	err := c.do(context.Background(), http.MethodPost, "/v1/networks/123456/adUnits", nil,
		map[string]string{"displayName": "x"}, nil)
	if err == nil {
		t.Fatal("expected a transport error, got nil")
	}
	if got := atomic.LoadInt32(&tr.calls); got != 1 {
		t.Errorf("transport saw %d attempts, want exactly 1 (writes are never retried on transport error)", got)
	}
}

func TestRetryRespectsContextCancellation(t *testing.T) {
	// While a retry is sleeping in sleepCtx (client.go:444) between attempts,
	// cancelling ctx must return promptly with the context error, not hang for
	// the full backoff. Use a long base delay so the sleep dominates.
	tr := &countingErrTransport{failN: -1}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c := testClient(t, srv, Config{})
	c.httpClient = &http.Client{Transport: tr}
	c.retryBaseDelay = 3 * time.Second // backoff(1) sleeps ~1.5-3s if not cancelled

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.GetNetwork(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Errorf("returned after %v; cancellation did not interrupt the backoff sleep", elapsed)
	}
	// Only the first attempt ran; cancellation struck during the backoff before
	// the second attempt could be made.
	if got := atomic.LoadInt32(&tr.calls); got != 1 {
		t.Errorf("transport saw %d attempts, want 1 (cancelled during backoff)", got)
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

func TestParseRetryAfterEdgeCases(t *testing.T) {
	// "0" seconds is a valid, honored delay of zero (secs >= 0 branch).
	if d, ok := parseRetryAfter("0"); !ok || d != 0 {
		t.Errorf(`parseRetryAfter("0") = %v, %v; want 0, true`, d, ok)
	}
	// A negative seconds value is not a valid Retry-After; it must be rejected
	// (and must not be misread as an HTTP date).
	if d, ok := parseRetryAfter("-5"); ok {
		t.Errorf(`parseRetryAfter("-5") = %v, %v; want _, false`, d, ok)
	}
	// A past HTTP-date means "retry now": ok is true with a zero delay
	// (client.go:439), never a negative sleep.
	past := time.Now().Add(-2 * time.Hour).UTC().Format(http.TimeFormat)
	if d, ok := parseRetryAfter(past); !ok || d != 0 {
		t.Errorf("parseRetryAfter(past date) = %v, %v; want 0, true", d, ok)
	}
	// Regression (found by FuzzParseRetryAfter): a seconds value large enough to
	// overflow time.Duration must NOT wrap to a negative delay. The policy is to
	// CLAMP such a value to maxRetryDelay (honored, bounded) rather than reject it,
	// so the caller still respects the server's "back off" intent without either
	// hammering the API or blocking for centuries.
	if d, ok := parseRetryAfter("9227000000"); !ok || d != maxRetryDelay {
		t.Errorf("parseRetryAfter(overflowing seconds) = %v, %v; want %v, true", d, ok, maxRetryDelay)
	}
	// The former overflow boundary (math.MaxInt64/time.Second = 9223372036) is no
	// longer a rejection edge: both the largest representable value and the first
	// overflowing one clamp to maxRetryDelay. A 292-year Retry-After is absurd; the
	// point is a bounded, non-hammering wait, not literal fidelity.
	if d, ok := parseRetryAfter("9223372036"); !ok || d != maxRetryDelay {
		t.Errorf("parseRetryAfter(9223372036) = %v, %v; want %v, true", d, ok, maxRetryDelay)
	}
	if d, ok := parseRetryAfter("9223372037"); !ok || d != maxRetryDelay {
		t.Errorf("parseRetryAfter(9223372037) = %v, %v; want %v, true", d, ok, maxRetryDelay)
	}
	// A huge-but-representable value (well under the overflow edge) also clamps: a
	// server asking for a multi-year wait must be bounded to maxRetryDelay too.
	if d, ok := parseRetryAfter("94608000"); !ok || d != maxRetryDelay { // 3 years in seconds
		t.Errorf("parseRetryAfter(3 years) = %v, %v; want %v, true", d, ok, maxRetryDelay)
	}
	// A value at or below the cap is honored verbatim: maxRetryDelay is 16s, so
	// "16" returns exactly 16s and "10" exactly 10s (no clamp).
	if d, ok := parseRetryAfter("16"); !ok || d != maxRetryDelay {
		t.Errorf("parseRetryAfter(16) = %v, %v; want %v, true", d, ok, maxRetryDelay)
	}
	if d, ok := parseRetryAfter("10"); !ok || d != 10*time.Second {
		t.Errorf("parseRetryAfter(10) = %v, %v; want 10s, true", d, ok)
	}
	// A far-future HTTP-date likewise clamps to maxRetryDelay.
	farFuture := time.Now().Add(72 * time.Hour).UTC().Format(http.TimeFormat)
	if d, ok := parseRetryAfter(farFuture); !ok || d != maxRetryDelay {
		t.Errorf("parseRetryAfter(far-future date) = %v, %v; want %v, true", d, ok, maxRetryDelay)
	}
}

func TestBackoffJitterAndOverflowClamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c := testClient(t, srv, Config{})
	c.retryBaseDelay = defaultRetryBaseDelay // 500ms; testClient shrinks it otherwise

	// For a normal attempt the delay is equal-jittered into [d/2, d).
	base := defaultRetryBaseDelay
	for i := 0; i < 500; i++ {
		got := c.backoff(1)
		if got < base/2 || got >= base {
			t.Fatalf("backoff(1) = %v, want in [%v, %v)", got, base/2, base)
		}
	}

	// A large attempt overflows the exponential term; the clamp (client.go:414)
	// must pin the delay to maxRetryDelay, keeping the jitter in
	// [maxRetryDelay/2, maxRetryDelay] and never producing a negative sleep.
	for _, attempt := range []int{30, 40, 62, 63, 64, 100} {
		for i := 0; i < 200; i++ {
			got := c.backoff(attempt)
			if got < maxRetryDelay/2 || got > maxRetryDelay {
				t.Fatalf("backoff(%d) = %v, want clamped into [%v, %v]", attempt, got, maxRetryDelay/2, maxRetryDelay)
			}
		}
	}
}

func TestNewResolvesInlineCredentials(t *testing.T) {
	// With no TokenSource, New must route Config.Credentials through
	// resolveCredentialsJSON + credentials.DetectDefault (client.go:141-156) —
	// the credential branch every other test bypasses. An authorized_user JSON
	// builds a token source lazily, so New succeeds without any network call.
	inline := `{"type":"authorized_user","client_id":"id.apps.googleusercontent.com",` +
		`"client_secret":"secret","refresh_token":"1//refresh"}`
	c, err := New(context.Background(), Config{NetworkCode: "123456", Credentials: inline})
	if err != nil {
		t.Fatalf("New with inline authorized_user credentials: %v", err)
	}
	if c == nil || c.httpClient == nil {
		t.Fatal("New returned a client without an HTTP client")
	}

	// A credentials blob DetectDefault cannot understand must surface a wrapped,
	// credential-free error from the same branch.
	badJSON := `{"type":"nonsense_type"}`
	if _, err := New(context.Background(), Config{NetworkCode: "123456", Credentials: badJSON}); err == nil {
		t.Fatal("expected an error for an unrecognized credentials type, got nil")
	} else if !strings.Contains(err.Error(), "resolving Google credentials") {
		t.Errorf("error = %v, want it wrapped as \"resolving Google credentials\"", err)
	}
}

func TestConcurrentCallsShareOneClient(t *testing.T) {
	// Fire many calls concurrently through a single *Client so the race detector
	// has real cross-goroutine work on the shared limiter and HTTP client. A
	// vacuous serial test would never exercise this.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = fmt.Fprint(w, `{"name":"networks/123456","networkCode":"123456"}`)
	}))
	defer srv.Close()

	c := testClient(t, srv, Config{RequestsPerSecond: 1000})

	const n = 16
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.GetNetwork(context.Background()); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent GetNetwork: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != n {
		t.Errorf("server saw %d calls, want %d", got, n)
	}
}

// FuzzParseRetryAfter throws arbitrary header values at parseRetryAfter (an
// untrusted-input boundary: the value comes straight off the wire). It must
// never panic, and whenever it reports a usable delay that delay must be
// non-negative — a negative sleep would corrupt the retry loop's timing.
func FuzzParseRetryAfter(f *testing.F) {
	for _, seed := range []string{
		"", "0", "3", "-5", "garbage",
		time.Now().UTC().Format(http.TimeFormat),
		time.Now().Add(-2 * time.Hour).UTC().Format(http.TimeFormat),
		"99999999999999999999", "  5  ", "Wed, 21 Oct 2015 07:28:00 GMT",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		d, ok := parseRetryAfter(value)
		// Invariant: a usable delay is always bounded to [0, maxRetryDelay]. A
		// negative sleep would corrupt the retry loop's timing; an unbounded one
		// would let a hostile or buggy server pin the client for centuries.
		if ok && (d < 0 || d > maxRetryDelay) {
			t.Fatalf("parseRetryAfter(%q) reported ok with an out-of-range delay %v (want 0..%v)", value, d, maxRetryDelay)
		}
	})
}
