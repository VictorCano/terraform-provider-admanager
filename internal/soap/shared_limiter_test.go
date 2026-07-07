package soap

// This test wires a real *client.Client to a soap.Client exactly as
// customTargetingValueResource.Configure does, then proves the two share ONE
// token bucket. No real Google credentials are used: a static fake token drives
// both, against local httptest fakes.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// TestRESTAndSOAPShareOneLimiter builds the SOAP shim from a REST client's
// accessors (HTTPClient/Limiter/NetworkCode/UserAgent) — the same wiring the
// provider uses — and proves REST reads and SOAP writes contend on the SAME
// limiter. The client is built with a bucket of one token that refills only
// hourly; a REST read consumes it, so a following SOAP write must block on the
// shared limiter and fail under a short deadline before reaching the server.
func TestRESTAndSOAPShareOneLimiter(t *testing.T) {
	restSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"networks/123456","networkCode":"123456"}`))
	}))
	defer restSrv.Close()

	var soapRequests int32
	soapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		soapRequests++
		_, _ = w.Write([]byte(createResponseXML))
	}))
	defer soapSrv.Close()

	// One token, then ~hourly refill: RequestsPerSecond = 1/3600, burst 1.
	c, err := client.New(context.Background(), client.Config{
		NetworkCode:       "123456",
		BaseURL:           restSrv.URL,
		RequestsPerSecond: 1.0 / 3600.0,
		TokenSource:       oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}),
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	// Wire the shim exactly like customTargetingValueResource.Configure.
	sc := NewClient(Config{
		HTTPClient:      c.HTTPClient(),
		Limiter:         c.Limiter(),
		NetworkCode:     c.NetworkCode(),
		ApplicationName: c.UserAgent(),
		BaseURL:         soapSrv.URL,
	})

	// A REST read consumes the single shared token.
	if _, err := c.GetNetwork(context.Background()); err != nil {
		t.Fatalf("GetNetwork drained the shared token: %v", err)
	}

	// The SOAP write now has no token; under a short deadline it must fail on the
	// shared limiter, so the SOAP server never sees the request. If REST and SOAP
	// had separate buckets, this write would sail through.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = sc.CreateCustomTargetingValue(ctx, Value{CustomTargetingKeyID: 1, Name: "x", MatchType: "EXACT"})
	if err == nil {
		t.Fatal("SOAP write succeeded despite the REST read draining the shared token — separate limiters?")
	}
	if soapRequests != 0 {
		t.Errorf("SOAP server saw %d requests, want 0 (the shared limiter must gate the write)", soapRequests)
	}
}

// TestConcurrentSOAPCallsShareOneClient fires many SOAP writes concurrently
// through one shim so the race detector has real cross-goroutine work on the
// shared HTTP client and limiter.
func TestConcurrentSOAPCallsShareOneClient(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(createResponseXML))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "") // limiter is rate.Inf, so concurrency is real

	const n = 12
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.CreateCustomTargetingValue(context.Background(),
				Value{CustomTargetingKeyID: 1, Name: "x", MatchType: "EXACT"}); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent SOAP create: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != n {
		t.Errorf("server saw %d requests, want %d", got, n)
	}
}
