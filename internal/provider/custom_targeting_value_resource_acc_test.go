package provider

// Acceptance tests for admanager_custom_targeting_value. These talk to a real Ad
// Manager test network and are skipped unless TF_ACC is set. They MUST run only
// against a network whose testNetwork flag is true; testAccPreCheck aborts
// otherwise.
//
// Writes go through the SOAP compatibility layer (internal/soap), so these tests
// exercise the create/update/deactivate SOAP path end to end while reads and the
// destroy check use REST.
//
// Required environment mirrors the ad_unit acceptance tests (see
// ad_unit_resource_acc_test.go).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// testAccCustomTargetingValueName returns a unique value string (the ad_tag_name)
// within the 40-character limit, drawn only from characters the API allows.
func testAccCustomTargetingValueName(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("generating random value name: %v", err)
	}
	return "tfval" + hex.EncodeToString(b[:]) // well under 40 chars.
}

func testAccCustomTargetingValueConfig(networkCode, keyAdTag, valueName, displayName string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_custom_targeting_key" "test" {
  ad_tag_name     = %[2]q
  type            = "PREDEFINED"
  reportable_type = "ON"
}

resource "admanager_custom_targeting_value" "test" {
  custom_targeting_key = admanager_custom_targeting_key.test.id
  ad_tag_name          = %[3]q
  display_name         = %[4]q
  match_type           = "EXACT"
}
`, networkCode, keyAdTag, valueName, displayName)
}

// testAccValueBareIDImportFunc returns the resource's bare numeric
// custom_targeting_value_id as the import ID, exercising
// normalizeCustomTargetingValueName's expansion of a bare id into a full
// resource name.
func testAccValueBareIDImportFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		id := rs.Primary.Attributes["custom_targeting_value_id"]
		if id == "" {
			return "", fmt.Errorf("resource %s has no custom_targeting_value_id in state", resourceName)
		}
		return id, nil
	}
}

// testAccCheckCustomTargetingValueInactive verifies that every value in state
// reads back INACTIVE (or is gone) after destroy — the documented destroy
// semantics for custom targeting values (deactivate via SOAP, since there is no
// archive or hard delete).
func testAccCheckCustomTargetingValueInactive(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		for name, rs := range s.RootModule().Resources {
			// Data sources share the type name; only managed resources count.
			if strings.HasPrefix(name, "data.") || rs.Type != "admanager_custom_targeting_value" {
				continue
			}
			v, err := c.GetCustomTargetingValue(context.Background(), rs.Primary.ID)
			if err != nil {
				if client.IsNotFound(err) {
					continue // gone counts as destroyed
				}
				return fmt.Errorf("reading custom targeting value %s after destroy: %w", rs.Primary.ID, err)
			}
			if v.Status != "INACTIVE" {
				return fmt.Errorf("custom targeting value %s status = %q after destroy, want INACTIVE", rs.Primary.ID, v.Status)
			}
		}
		return nil
	}
}

func TestAccCustomTargetingValueResource_basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	keyAdTag := testAccCustomTargetingKeyAdTagName(t)
	valueName := testAccCustomTargetingValueName(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// Default destroy must deactivate the value AND the parent key
		// (documented GAM semantics: values and keys deactivate, never hard delete).
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			testAccCheckCustomTargetingValueInactive(t),
			testAccCheckCustomTargetingKeyInactive(t),
		),
		Steps: []resource.TestStep{
			{
				// Create (SOAP create + REST read-back).
				Config: testAccCustomTargetingValueConfig(code, keyAdTag, valueName, "Honda"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("admanager_custom_targeting_value.test", "ad_tag_name", valueName),
					resource.TestCheckResourceAttr("admanager_custom_targeting_value.test", "display_name", "Honda"),
					resource.TestCheckResourceAttr("admanager_custom_targeting_value.test", "match_type", "EXACT"),
					resource.TestCheckResourceAttrSet("admanager_custom_targeting_value.test", "id"),
					resource.TestCheckResourceAttrSet("admanager_custom_targeting_value.test", "custom_targeting_value_id"),
					resource.TestCheckResourceAttrPair(
						"admanager_custom_targeting_value.test", "custom_targeting_key",
						"admanager_custom_targeting_key.test", "id"),
					resource.TestCheckResourceAttr("admanager_custom_targeting_value.test", "status", "ACTIVE"),
				),
			},
			{
				// Update the mutable display_name in place (SOAP update + REST read).
				Config: testAccCustomTargetingValueConfig(code, keyAdTag, valueName, "Honda Updated"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("admanager_custom_targeting_value.test", "display_name", "Honda Updated"),
				),
			},
			{
				// Import by full resource name; state must match.
				ResourceName:            "admanager_custom_targeting_value.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"skip_archive_on_destroy"},
			},
			{
				// Import by bare numeric value ID (exercises
				// normalizeCustomTargetingValueName's expansion to a full name).
				ResourceName:            "admanager_custom_targeting_value.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateIdFunc:       testAccValueBareIDImportFunc("admanager_custom_targeting_value.test"),
				ImportStateVerifyIgnore: []string{"skip_archive_on_destroy"},
			},
		},
	})
}
