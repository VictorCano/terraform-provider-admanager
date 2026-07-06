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
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "admanager_custom_targeting_key" {
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
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("admanager_custom_targeting_key.test", "ad_tag_name", adTagName),
					resource.TestCheckResourceAttr("admanager_custom_targeting_key.test", "display_name", "Genre"),
					resource.TestCheckResourceAttr("admanager_custom_targeting_key.test", "type", "FREEFORM"),
					resource.TestCheckResourceAttr("admanager_custom_targeting_key.test", "reportable_type", "ON"),
					resource.TestCheckResourceAttrSet("admanager_custom_targeting_key.test", "id"),
					resource.TestCheckResourceAttrSet("admanager_custom_targeting_key.test", "custom_targeting_key_id"),
					resource.TestCheckResourceAttr("admanager_custom_targeting_key.test", "status", "ACTIVE"),
				),
			},
			{
				// Update the display name in place (patch with a field mask); the
				// immutable ad_tag_name is unchanged.
				Config: testAccCustomTargetingKeyConfig(code, adTagName, "Genre Updated"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("admanager_custom_targeting_key.test", "display_name", "Genre Updated"),
				),
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
