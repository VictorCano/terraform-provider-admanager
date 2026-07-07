//go:build reservedcode

package provider

// MANUAL-ONLY reserved-code collision end-to-end test (P1-3).
//
// This test DELIBERATELY triggers the ad_unit_code reservation trap documented
// on the ad_unit_code attribute (ad_unit_resource.go) and diagnosed by the
// create-collision handler (issue #2): an archived ad unit keeps its
// ad_unit_code reserved, so creating a NEW ad unit that reuses that code fails
// with HTTP 400 (Ad Manager reports UNIQUE_ERROR_NOT_UNIQUE / "Field is not
// unique"). The scenario is verified against the live network: an out-of-band
// holder unit is created with a unique code and archived, then Terraform is
// asked to create a fresh unit reusing that exact code — which must fail.
//
// Note on shape: the mitigation warning ("Replacing this ad unit archives it and
// reserves its ad_unit_code") is emitted by ModifyPlan on a REPLACE, and is
// asserted credential-free in the in-process ModifyPlan test (P0-8). Acceptance
// cannot assert a plan-time warning on an ExpectError step, and empirically a
// same-resource reparent replace does NOT reproduce the collision on this
// network (Terraform's replace ordering lets the create reuse the code), so this
// e2e reproduces the trap via the deterministic, documented path — a fresh
// create reusing an archived holder's code — and asserts the API-side error.
//
// It is gated behind the `reservedcode` build tag and is NEVER part of the
// always-run suite or the weekly cron: every run PERMANENTLY reserves the code
// it uses (archiving never un-reserves a code, and the sweeper cannot reclaim it
// — see sweeper_test.go), so it uses a UNIQUE per-run code and must be run by
// hand only:
//
//	TF_ACC=1 ADMANAGER_TEST_NETWORK_CODE=... GOOGLE_APPLICATION_CREDENTIALS=... \
//	  go test -tags reservedcode -run TestAccAdUnitResource_reservedCodeCollision -v ./internal/provider
//
// The normal suite does not compile this file (no build tag), so `go test ./...`
// skips it entirely.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// testAccReservedCodeUnique returns a unique per-run ad_unit_code. A fresh code
// each run is mandatory: a fixed one would collide on the SECOND run (the first
// run's archived holder permanently reserves it), turning a deliberate one-shot
// test into a permanent failure.
func testAccReservedCodeUnique(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("generating unique reserved code: %v", err)
	}
	return "tf_rc_" + hex.EncodeToString(b[:])
}

// testAccReservedCodeReuseConfig declares a single ad unit that reuses code,
// which an out-of-band archived holder already owns. Its create must fail.
func testAccReservedCodeReuseConfig(networkCode, parent, displayName, code string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_ad_unit" "reuse" {
  parent_ad_unit = %[2]q
  display_name   = %[3]q
  ad_unit_code   = %[4]q
}
`, networkCode, parent, displayName, code)
}

// testAccCheckReservedCodeCleanup best-effort archives every ad unit left in
// state, tolerating already-archived units. This test intentionally ends in a
// failed apply, so cleanup is lenient (it never fails the test) — its only job is
// to satisfy the CheckDestroy hygiene guard.
func testAccCheckReservedCodeCleanup(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		for name, rs := range s.RootModule().Resources {
			if strings.HasPrefix(name, "data.") || rs.Type != "admanager_ad_unit" {
				continue
			}
			if err := c.ArchiveAdUnit(context.Background(), rs.Primary.ID); err != nil {
				t.Logf("cleanup: archiving ad unit %s: %v", rs.Primary.ID, err)
			}
		}
		return nil
	}
}

func TestAccAdUnitResource_reservedCodeCollision(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	name := acctest.RandomWithPrefix("tf-acc-adunit-reservedcode")
	adUnitCode := testAccReservedCodeUnique(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReservedCodeCleanup(t),
		Steps: []resource.TestStep{
			{
				// Establish (out of band) an ARCHIVED holder that permanently
				// reserves adUnitCode, then ask Terraform to create a fresh unit that
				// reuses it — the create must fail with the uniqueness/collision
				// error. Doing the holder setup in PreConfig keeps it off Terraform
				// state so the ExpectError step has exactly one managed resource.
				PreConfig: func() {
					c := testAccClient(t)
					holder, err := c.CreateAdUnit(context.Background(), &client.AdUnit{
						ParentAdUnit: root,
						DisplayName:  name + "-holder",
						AdUnitCode:   adUnitCode,
					})
					if err != nil {
						t.Fatalf("creating reserved-code holder: %v", err)
					}
					if err := c.ArchiveAdUnit(context.Background(), holder.Name); err != nil {
						t.Fatalf("archiving reserved-code holder %s: %v", holder.Name, err)
					}
				},
				Config:      testAccReservedCodeReuseConfig(code, root, name, adUnitCode),
				ExpectError: regexp.MustCompile(`(?i)(not unique|UNIQUE|INVALID_ARGUMENT|already held by|reserved)`),
			},
		},
	})
}
