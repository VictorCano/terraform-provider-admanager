package provider

// Acceptance tests for admanager_placement. These talk to a real Ad Manager
// test network and are skipped unless TF_ACC is set. They MUST run only against
// a network whose testNetwork flag is true; testAccPreCheck aborts otherwise.
//
// Required environment mirrors the ad_unit acceptance tests (see
// ad_unit_resource_acc_test.go): ADMANAGER_TEST_NETWORK_CODE (or
// ADMANAGER_NETWORK_CODE) plus GOOGLE_CREDENTIALS_JSON or
// GOOGLE_APPLICATION_CREDENTIALS.

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccPlacementConfig creates an ad unit and a placement that targets it,
// exercising the resource-reference flow (targeted_ad_units references the ad
// unit's id directly).
func testAccPlacementConfig(networkCode, parent, adUnitName, placementName string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_ad_unit" "target" {
  parent_ad_unit = %[2]q
  display_name   = %[3]q
}

resource "admanager_placement" "test" {
  display_name      = %[4]q
  description       = "Managed by acceptance tests"
  targeted_ad_units = [admanager_ad_unit.target.id]
}
`, networkCode, parent, adUnitName, placementName)
}

// testAccCheckPlacementArchived verifies that every placement in state reads
// back as ARCHIVED after destroy (the documented default destroy semantics),
// and best-effort archives any ad units left behind so the test network does
// not accumulate live inventory.
func testAccCheckPlacementArchived(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		for _, rs := range s.RootModule().Resources {
			switch rs.Type {
			case "admanager_placement":
				p, err := c.GetPlacement(context.Background(), rs.Primary.ID)
				if err != nil {
					return fmt.Errorf("reading placement %s after destroy: %w", rs.Primary.ID, err)
				}
				if p.Status != "ARCHIVED" {
					return fmt.Errorf("placement %s status = %q after destroy, want ARCHIVED", rs.Primary.ID, p.Status)
				}
			case "admanager_ad_unit":
				au, err := c.GetAdUnit(context.Background(), rs.Primary.ID)
				if err != nil {
					return fmt.Errorf("reading ad unit %s after destroy: %w", rs.Primary.ID, err)
				}
				if au.Status != "ARCHIVED" {
					return fmt.Errorf("ad unit %s status = %q after destroy, want ARCHIVED", rs.Primary.ID, au.Status)
				}
			}
		}
		return nil
	}
}

func TestAccPlacementResource_basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	adUnitName := acctest.RandomWithPrefix("tf-acc-placement-au")
	name := acctest.RandomWithPrefix("tf-acc-placement")
	updated := name + "-updated"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// Default destroy must archive both the placement and the ad unit.
		CheckDestroy: testAccCheckPlacementArchived(t),
		Steps: []resource.TestStep{
			{
				// Create ad unit + placement targeting it.
				Config: testAccPlacementConfig(code, root, adUnitName, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("admanager_placement.test", "display_name", name),
					resource.TestCheckResourceAttrSet("admanager_placement.test", "id"),
					resource.TestCheckResourceAttrSet("admanager_placement.test", "placement_id"),
					resource.TestCheckResourceAttrSet("admanager_placement.test", "placement_code"),
					resource.TestCheckResourceAttr("admanager_placement.test", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("admanager_placement.test", "targeted_ad_units.#", "1"),
					// targeted_ad_units is a set: elements are addressed by the "*"
					// wildcard, not a stable numeric index.
					resource.TestCheckTypeSetElemAttrPair(
						"admanager_placement.test", "targeted_ad_units.*",
						"admanager_ad_unit.target", "id"),
				),
			},
			{
				// Update the display name in place (patch with a field mask).
				Config: testAccPlacementConfig(code, root, adUnitName, updated),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("admanager_placement.test", "display_name", updated),
				),
			},
			{
				// Import by full resource name; state must match.
				ResourceName:            "admanager_placement.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"skip_archive_on_destroy"},
			},
		},
	})
}
