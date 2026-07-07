package provider

// Acceptance tests for admanager_custom_targeting_key. These talk to a real Ad
// Manager test network and are skipped unless TF_ACC is set. They MUST run only
// against a network whose testNetwork flag is true; testAccPreCheck aborts
// otherwise.
//
// Required environment mirrors the ad_unit acceptance tests (see
// ad_unit_resource_acc_test.go).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// testAccCustomTargetingKeyAdTagName returns a unique adTagName that respects
// the API's constraints: at most 10 characters, drawn only from characters the
// API allows (alphanumerics here). It is "tf" plus 8 hex characters = 10 chars.
//
// Uniqueness matters: Ad Manager may reject re-creating a key whose adTagName
// matches an existing (even deactivated) key, so every run uses a fresh name
// rather than a fixed one that a prior destroyed-but-deactivated run left behind.
func testAccCustomTargetingKeyAdTagName(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("generating random ad_tag_name: %v", err)
	}
	return "tf" + hex.EncodeToString(b[:]) // "tf" + 8 hex chars = 10 chars.
}

func testAccCustomTargetingKeyConfig(networkCode, adTagName, displayName string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_custom_targeting_key" "test" {
  ad_tag_name     = %[2]q
  display_name    = %[3]q
  type            = "FREEFORM"
  reportable_type = "ON"
}
`, networkCode, adTagName, displayName)
}

// testAccCheckCustomTargetingKeyInactive verifies that every key in state reads
// back as INACTIVE after destroy — the documented default destroy semantics for
// custom targeting keys (deactivate, since there is no archive or hard delete).
func testAccCheckCustomTargetingKeyInactive(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		for name, rs := range s.RootModule().Resources {
			// Data sources share the type name; only managed resources count.
			if strings.HasPrefix(name, "data.") || rs.Type != "admanager_custom_targeting_key" {
				continue
			}
			k, err := c.GetCustomTargetingKey(context.Background(), rs.Primary.ID)
			if err != nil {
				return fmt.Errorf("reading custom targeting key %s after destroy: %w", rs.Primary.ID, err)
			}
			if k.Status != "INACTIVE" {
				return fmt.Errorf("custom targeting key %s status = %q after destroy, want INACTIVE", rs.Primary.ID, k.Status)
			}
		}
		return nil
	}
}

func TestAccCustomTargetingKeyResource_basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	adTagName := testAccCustomTargetingKeyAdTagName(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// Default destroy must deactivate the key (documented GAM semantics).
		CheckDestroy: testAccCheckCustomTargetingKeyInactive(t),
		Steps: []resource.TestStep{
			{
				// Create.
				Config: testAccCustomTargetingKeyConfig(code, adTagName, "Genre"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("ad_tag_name"), knownvalue.StringExact(adTagName)),
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("display_name"), knownvalue.StringExact("Genre")),
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("type"), knownvalue.StringExact("FREEFORM")),
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("reportable_type"), knownvalue.StringExact("ON")),
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("custom_targeting_key_id"), knownvalue.StringRegexp(numericIDRegexp)),
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("id"), knownvalue.NotNull()),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Update the display name: must plan as an in-place update (the
				// immutable ad_tag_name is unchanged, so this is not a replace).
				Config: testAccCustomTargetingKeyConfig(code, adTagName, "Genre Updated"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("admanager_custom_targeting_key.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("display_name"), knownvalue.StringExact("Genre Updated")),
				},
			},
			{
				// Import by full resource name; state must match.
				ResourceName:            "admanager_custom_targeting_key.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"skip_archive_on_destroy"},
			},
		},
	})
}

// testAccCustomTargetingKeyDuplicateConfig declares the standard `.test` key
// plus a second `.dup` key that reuses the SAME ad_tag_name. Ad Manager enforces
// ad_tag_name uniqueness per network, so creating `.dup` is rejected by the API.
func testAccCustomTargetingKeyDuplicateConfig(networkCode, adTagName, displayName string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_custom_targeting_key" "test" {
  ad_tag_name     = %[2]q
  display_name    = %[3]q
  type            = "FREEFORM"
  reportable_type = "ON"
}

resource "admanager_custom_targeting_key" "dup" {
  ad_tag_name     = %[2]q
  display_name    = "Duplicate ad_tag_name"
  type            = "FREEFORM"
  reportable_type = "ON"
}
`, networkCode, adTagName, displayName)
}

// TestAccCustomTargetingKeyResource_duplicateAdTagName is the sole residual
// acceptance-side ExpectError case (P1-2): it asserts a genuinely API-side
// validation error that no schema or plan validator can catch. The ad_tag_name
// denylist and length limits are enforced at plan time (covered credential-free
// in L3), but uniqueness of an ad_tag_name against an already-existing key is a
// server-side invariant — the plan cannot know another key already claims the
// name. Step 1 creates the key; step 2 adds a second key with the same
// ad_tag_name and expects the create to fail. CheckDestroy deactivates whatever
// remains, and the harness's teardown destroys all created state, so the case is
// leak-free even if the API were ever to accept the duplicate.
func TestAccCustomTargetingKeyResource_duplicateAdTagName(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	adTagName := testAccCustomTargetingKeyAdTagName(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCustomTargetingKeyInactive(t),
		Steps: []resource.TestStep{
			{
				// Establish the first key that owns the ad_tag_name.
				Config: testAccCustomTargetingKeyConfig(code, adTagName, "Genre"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
				},
			},
			{
				// A second key with the same ad_tag_name must be rejected by the
				// API. No Check/ConfigStateChecks here: the harness silently
				// ignores them on an ExpectError step (acc_test_guard rule b).
				Config:      testAccCustomTargetingKeyDuplicateConfig(code, adTagName, "Genre"),
				ExpectError: regexp.MustCompile(`(?i)(already exists|not unique|duplicat|CUSTOM_TARGETING_KEY|KEY_NAME|uniqu)`),
			},
		},
	})
}
