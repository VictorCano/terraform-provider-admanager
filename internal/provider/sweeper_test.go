package provider

// Sweeper for the single shared Ad Manager test network, plus the package
// TestMain that wires the terraform-plugin-testing sweeper runner.
//
// # Charter and hard limit
//
// The sweeper reclaims objects that a crashed or interrupted acceptance run left
// behind on the ONE shared test network. Its charter ENDS AT ARCHIVE/DEACTIVATE:
// it archives dangling ad units and placements and deactivates dangling custom
// targeting keys and values. It NEVER hard-deletes anything (locked decision 4 —
// the Ad Manager API has no hard delete for these entities and the provider must
// not pretend otherwise).
//
// One liability is explicitly OUT OF SCOPE and un-sweepable: an archived ad unit
// keeps its ad_unit_code reserved FOREVER, and archive never un-reserves it. The
// sweep cannot reclaim a reserved code. Acceptance tests must therefore NEVER pin
// a fixed ad_unit_code (always let GAM auto-assign, or use a unique per-run code
// in the manual-only reserved-code e2e) so that reserved codes do not accumulate
// into a fixed-name collision on a later run.
//
// # Hard guard (non-negotiable)
//
// A sweeper runs OUTSIDE testAccPreCheck, so it re-implements the golden-rule-3
// guard itself: every sweeper reads the network and refuses to touch anything
// unless Network.TestNetwork == true. Sweeping a production network is a
// catastrophic, un-undoable action; the guard is the only thing standing between
// -sweep and that outcome.
//
// # Invocation
//
//	go test ./internal/provider -v -sweep=global
//	go test ./internal/provider -v -sweep=global -sweep-run=admanager_ad_unit
//
// Ad Manager has no regions, but the -sweep flag requires a region argument, so
// "global" is a placeholder and each sweep func ignores its region argument.
// Dependency order is encoded so the runner archives placements before the ad
// units they target, and deactivates values before their parent keys.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
	"github.com/VictorCano/terraform-provider-admanager/internal/soap"
)

// Sweeper names. They double as the resource type names so -sweep-run reads
// naturally (e.g. -sweep-run=admanager_ad_unit).
const (
	sweeperAdUnit               = "admanager_ad_unit"
	sweeperPlacement            = "admanager_placement"
	sweeperCustomTargetingKey   = "admanager_custom_targeting_key"
	sweeperCustomTargetingValue = "admanager_custom_targeting_value"
)

// maxSweepPages bounds every paginated list the sweeper performs so a
// misconfigured filter or a huge network cannot loop unbounded. The test network
// is small; this cap is generous.
const maxSweepPages = 50

// keyAdTagPattern matches the adTagName the key acceptance tests generate:
// "tf" followed by exactly 8 hex characters (testAccCustomTargetingKeyAdTagName).
// It deliberately does NOT match a value adTagName ("tfval" + 12 hex) — the 'v'
// is not a hex digit — nor an ad-unit/placement display name ("tf-acc-...").
var keyAdTagPattern = regexp.MustCompile(`^tf[0-9a-f]{8}$`)

// isSweepableAccName reports whether s (an object's display_name or adTagName)
// carries one of the naming prefixes this acceptance suite actually produces:
//
//   - "tf-acc-" / "tf_acc_": ad unit & placement display names (and data-source
//     fixtures) via acctest.RandomWithPrefix.
//   - "tfval": custom targeting value adTagName (testAccCustomTargetingValueName).
//   - "tf" + 8 hex: custom targeting key adTagName (keyAdTagPattern).
//   - "probe ": reserved prefix for any transient probe fixture.
//
// It is the single decision point the sweepers share, so it is unit-tested
// credential-free (TestIsSweepableAccName) rather than only via a live sweep.
func isSweepableAccName(s string) bool {
	switch {
	case strings.HasPrefix(s, "tf-acc-"),
		strings.HasPrefix(s, "tf_acc_"),
		strings.HasPrefix(s, "tfval"),
		strings.HasPrefix(s, "probe "):
		return true
	}
	return keyAdTagPattern.MatchString(s)
}

// TestMain registers the sweepers and hands control to the plugin-testing runner.
// With -sweep it runs the sweepers; without it, it runs the package's tests
// normally.
func TestMain(m *testing.M) {
	resource.AddTestSweepers(sweeperPlacement, &resource.Sweeper{
		Name: sweeperPlacement,
		F:    sweepPlacements,
	})
	resource.AddTestSweepers(sweeperAdUnit, &resource.Sweeper{
		Name: sweeperAdUnit,
		// Placements target ad units, so archive placements first.
		Dependencies: []string{sweeperPlacement},
		F:            sweepAdUnits,
	})
	resource.AddTestSweepers(sweeperCustomTargetingValue, &resource.Sweeper{
		Name: sweeperCustomTargetingValue,
		F:    sweepCustomTargetingValues,
	})
	resource.AddTestSweepers(sweeperCustomTargetingKey, &resource.Sweeper{
		Name: sweeperCustomTargetingKey,
		// Values belong to keys, so deactivate values first.
		Dependencies: []string{sweeperCustomTargetingValue},
		F:            sweepCustomTargetingKeys,
	})
	resource.TestMain(m)
}

// newSweeperClient builds a REST client from the environment, mirroring
// testAccClient but without a *testing.T (a sweeper func has none). It resolves
// credentials the same way the provider does: inline GOOGLE_CREDENTIALS_JSON if
// set, otherwise ADC (which honors GOOGLE_APPLICATION_CREDENTIALS).
func newSweeperClient() (*client.Client, error) {
	code := os.Getenv("ADMANAGER_TEST_NETWORK_CODE")
	if code == "" {
		code = os.Getenv("ADMANAGER_NETWORK_CODE")
	}
	if code == "" {
		return nil, fmt.Errorf("ADMANAGER_TEST_NETWORK_CODE (or ADMANAGER_NETWORK_CODE) must be set to sweep")
	}
	return client.New(context.Background(), client.Config{
		NetworkCode: code,
		Credentials: os.Getenv("GOOGLE_CREDENTIALS_JSON"),
	})
}

// sweeperGuardedClient builds the client and enforces the golden-rule-3 guard:
// it refuses to return a usable client unless the network's testNetwork flag is
// true. Every sweeper calls this first, so no sweep can ever run against a
// production network even though sweepers run outside testAccPreCheck.
func sweeperGuardedClient(ctx context.Context) (*client.Client, error) {
	c, err := newSweeperClient()
	if err != nil {
		return nil, err
	}
	net, err := c.GetNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("sweeper: reading network before sweep: %w", err)
	}
	if !net.TestNetwork {
		return nil, fmt.Errorf(
			"sweeper: refusing to sweep non-test network %s; sweeps only run against a network whose testNetwork flag is true",
			net.NetworkCode)
	}
	return c, nil
}

// sweeperBaseURL is the REST endpoint the sweeper lists against. The REST client
// does not expose its base URL and there is no list method for placements/keys/
// values, so the sweeper issues its own authenticated GETs through the client's
// shared oauth2 HTTP client and token bucket (it never bypasses the limiter). An
// env override exists only so the guarded sweep can be pointed at a fake server
// in a future in-process test; production runs use the default.
func sweeperBaseURL() string {
	if v := os.Getenv("ADMANAGER_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://admanager.googleapis.com"
}

// sweeperListPage performs one authenticated, rate-limited GET and decodes the
// JSON body into out. It waits on the shared limiter first so sweep traffic
// draws from the same 2 rps budget as everything else (locked decision 3).
func sweeperListPage(ctx context.Context, c *client.Client, urlStr string, out any) error {
	if err := c.Limiter().Wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sweeper: list %s returned HTTP %d", urlStr, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// sweepCollection lists every object in a network collection (adUnits,
// placements, customTargetingKeys, customTargetingValues), following
// nextPageToken up to maxSweepPages, and returns the raw page bodies' decoded
// items. It is generic over the item type via the caller-supplied decode step.
func sweepCollection[T any](ctx context.Context, c *client.Client, collection string, items func(page json.RawMessage) ([]T, string, error)) ([]T, error) {
	var out []T
	base := sweeperBaseURL() + "/v1/networks/" + c.NetworkCode() + "/" + collection
	pageToken := ""
	for page := 1; ; page++ {
		urlStr := sweeperPageURL(base, pageToken)
		var raw json.RawMessage
		if err := sweeperListPage(ctx, c, urlStr, &raw); err != nil {
			return nil, err
		}
		got, next, err := items(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		if next == "" {
			return out, nil
		}
		pageToken = next
		if page >= maxSweepPages {
			return nil, fmt.Errorf("sweeper: listing %s stopped at the %d-page cap", collection, maxSweepPages)
		}
	}
}

// sweeperPageURL builds the next-page request URL for a paginated sweep list.
// It routes the opaque continuation token through net/url so it is percent-
// encoded, matching how the rest of the codebase emits pageToken (ad_unit.go's
// ListAdUnits and client.do both use url.Values). Raw concatenation would let a
// token containing '+' or '&' malform the request and silently truncate the
// sweep, leaving dangling objects unswept.
func sweeperPageURL(base, pageToken string) string {
	if pageToken == "" {
		return base
	}
	q := url.Values{}
	q.Set("pageToken", pageToken)
	return base + "?" + q.Encode()
}

func sweepAdUnits(_ string) error {
	ctx := context.Background()
	c, err := sweeperGuardedClient(ctx)
	if err != nil {
		return err
	}
	// Ad units have a first-class list method; use it (it paginates and caps).
	units, err := c.ListAdUnits(ctx, client.ListAdUnitsOptions{})
	if err != nil {
		return fmt.Errorf("sweeper: listing ad units: %w", err)
	}
	var errs []string
	for i := range units {
		au := units[i]
		if !isSweepableAccName(au.DisplayName) || au.Status == "ARCHIVED" {
			continue
		}
		if archiveErr := c.ArchiveAdUnit(ctx, au.Name); archiveErr != nil {
			errs = append(errs, fmt.Sprintf("archiving ad unit %s: %v", au.Name, archiveErr))
		}
	}
	return sweepErr("ad units", errs)
}

func sweepPlacements(_ string) error {
	ctx := context.Background()
	c, err := sweeperGuardedClient(ctx)
	if err != nil {
		return err
	}
	placements, err := sweepCollection(ctx, c, "placements", func(raw json.RawMessage) ([]client.Placement, string, error) {
		var body struct {
			Placements    []client.Placement `json:"placements"`
			NextPageToken string             `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", err
		}
		return body.Placements, body.NextPageToken, nil
	})
	if err != nil {
		return fmt.Errorf("sweeper: listing placements: %w", err)
	}
	var errs []string
	for i := range placements {
		p := placements[i]
		if !isSweepableAccName(p.DisplayName) || p.Status == "ARCHIVED" {
			continue
		}
		if archiveErr := c.ArchivePlacement(ctx, p.Name); archiveErr != nil {
			errs = append(errs, fmt.Sprintf("archiving placement %s: %v", p.Name, archiveErr))
		}
	}
	return sweepErr("placements", errs)
}

func sweepCustomTargetingKeys(_ string) error {
	ctx := context.Background()
	c, err := sweeperGuardedClient(ctx)
	if err != nil {
		return err
	}
	keys, err := sweepCollection(ctx, c, "customTargetingKeys", func(raw json.RawMessage) ([]client.CustomTargetingKey, string, error) {
		var body struct {
			CustomTargetingKeys []client.CustomTargetingKey `json:"customTargetingKeys"`
			NextPageToken       string                      `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", err
		}
		return body.CustomTargetingKeys, body.NextPageToken, nil
	})
	if err != nil {
		return fmt.Errorf("sweeper: listing custom targeting keys: %w", err)
	}
	var errs []string
	for i := range keys {
		k := keys[i]
		if !isSweepableAccName(k.AdTagName) || k.Status != "ACTIVE" {
			continue
		}
		if deErr := c.DeactivateCustomTargetingKey(ctx, k.Name); deErr != nil {
			errs = append(errs, fmt.Sprintf("deactivating key %s: %v", k.Name, deErr))
		}
	}
	return sweepErr("custom targeting keys", errs)
}

func sweepCustomTargetingValues(_ string) error {
	ctx := context.Background()
	c, err := sweeperGuardedClient(ctx)
	if err != nil {
		return err
	}
	values, err := sweepCollection(ctx, c, "customTargetingValues", func(raw json.RawMessage) ([]client.CustomTargetingValue, string, error) {
		var body struct {
			CustomTargetingValues []client.CustomTargetingValue `json:"customTargetingValues"`
			NextPageToken         string                        `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", err
		}
		return body.CustomTargetingValues, body.NextPageToken, nil
	})
	if err != nil {
		return fmt.Errorf("sweeper: listing custom targeting values: %w", err)
	}
	// Value deactivation goes through SOAP, built from the REST client's shared
	// HTTP client and limiter (no parallel auth, no second token bucket).
	sc := soap.NewClient(soap.Config{
		HTTPClient:      c.HTTPClient(),
		Limiter:         c.Limiter(),
		NetworkCode:     c.NetworkCode(),
		ApplicationName: c.UserAgent(),
	})
	var errs []string
	for i := range values {
		v := values[i]
		if !isSweepableAccName(v.AdTagName) || v.Status != "ACTIVE" {
			continue
		}
		keyID, keyErr := soap.KeyIDFromResourceName(v.CustomTargetingKey)
		if keyErr != nil {
			errs = append(errs, fmt.Sprintf("value %s: parsing key id: %v", v.Name, keyErr))
			continue
		}
		valueID, valErr := soap.ValueIDFromResourceName(v.Name)
		if valErr != nil {
			errs = append(errs, fmt.Sprintf("value %s: parsing value id: %v", v.Name, valErr))
			continue
		}
		if _, deErr := sc.DeleteCustomTargetingValue(ctx, keyID, valueID); deErr != nil {
			// A value whose parent key is already INACTIVE cannot be deactivated
			// (Ad Manager rejects it with KEY_STATUS_NOT_ACTIVE) and there is no
			// activate path to unstick it. Such a value is effectively parked (its
			// inactive key means it cannot serve), so tolerate it rather than fail
			// the whole sweep — otherwise one un-reclaimable orphan wedges the gate.
			if isKeyStatusNotActive(deErr) {
				log.Printf("[WARN] sweeper: value %s left ACTIVE under an inactive key (un-deactivatable): %v", v.Name, deErr)
				continue
			}
			errs = append(errs, fmt.Sprintf("deactivating value %s: %v", v.Name, deErr))
		}
	}
	return sweepErr("custom targeting values", errs)
}

// isKeyStatusNotActive reports whether err is the SOAP fault Ad Manager returns
// when deactivating a value whose parent key is already inactive.
func isKeyStatusNotActive(err error) bool {
	var se *soap.SOAPError
	if !errors.As(err, &se) {
		return false
	}
	for _, e := range se.Errors {
		if e.Reason == "KEY_STATUS_NOT_ACTIVE" || strings.Contains(e.ErrorString, "KEY_STATUS_NOT_ACTIVE") {
			return true
		}
	}
	return false
}

// sweepErr collapses the per-object failures of one sweep into a single error,
// or nil if the sweep was clean.
func sweepErr(kind string, errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("sweeping %s: %s", kind, strings.Join(errs, "; "))
}

// TestSweeperPageURL pins that the sweeper's next-page URL percent-encodes the
// continuation token, matching the rest of the codebase (which routes pageToken
// through net/url). An opaque token containing '+', '&', '/', or '=' must not be
// placed raw on the wire, where the server would misparse it and silently
// truncate the sweep — leaving dangling objects unswept on the shared network.
func TestSweeperPageURL(t *testing.T) {
	const base = "https://admanager.googleapis.com/v1/networks/123/placements"

	if got := sweeperPageURL(base, ""); got != base {
		t.Errorf("sweeperPageURL(base, \"\") = %q, want %q (no query when token empty)", got, base)
	}

	// A token with query-breaking characters must survive a round-trip through
	// URL parsing unchanged.
	want := "a+b/c=d&e"
	got := sweeperPageURL(base, want)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("sweeperPageURL produced an unparseable URL %q: %v", got, err)
	}
	if u.Query().Get("pageToken") != want {
		t.Errorf("round-tripped pageToken = %q, want %q (URL %q)", u.Query().Get("pageToken"), want, got)
	}
}

// TestIsSweepableAccName pins the prefix filter that decides what the sweeper is
// allowed to touch. It is the credential-free guard against the sweeper matching
// (and archiving/deactivating) an object this suite did not create — the sweep
// runs on a shared network, so a too-broad match is the dangerous failure mode.
func TestIsSweepableAccName(t *testing.T) {
	sweepable := []string{
		"tf-acc-adunit-abc123", // ad unit / placement display name
		"tf-acc-placement",     //
		"tf_acc_ds_code456",    // data-source fixture
		"tfval0123456789ab",    // custom targeting value adTagName
		"tf0123abcd",           // custom targeting key adTagName (tf + 8 hex)
		"tfdeadbeef",           // key adTagName, all-hex tail
		"probe network-canary", // reserved probe prefix
	}
	for _, name := range sweepable {
		if !isSweepableAccName(name) {
			t.Errorf("isSweepableAccName(%q) = false, want true (suite-created object must be sweepable)", name)
		}
	}

	protected := []string{
		"Genre",              // key display name — never the match field for keys
		"Honda",              // value display name
		"production-ad-unit", // real inventory
		"tf0123abc",          // only 7 hex — not the key pattern, not tfval
		"tf0123abcde",        // 9 chars after tf — not the exact key pattern
		"tfXYZ",              // non-hex tail, not tfval
		"terraform",          // starts with "tf" but is neither pattern
		"",                   // empty
		// Mid-string markers: these carry a sweepable marker somewhere OTHER than
		// the start, so they must stay protected. They pin the prefix-ANCHORING of
		// the matcher: a regression that widened it to substring matching
		// (strings.Contains) would archive/deactivate these legitimate objects and
		// must fail this test.
		"Q3 tf-acc-old-campaign backup", // "tf-acc-" mid-string
		"legacy tf_acc_ report",         // "tf_acc_" mid-string
		"archived tfval backup",         // "tfval" mid-string
		"network probe canary",          // "probe " mid-string
	}
	for _, name := range protected {
		if isSweepableAccName(name) {
			t.Errorf("isSweepableAccName(%q) = true, want false (must not sweep objects the suite did not create)", name)
		}
	}
}
