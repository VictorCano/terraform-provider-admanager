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
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
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
		for name, rs := range s.RootModule().Resources {
			// Data sources share type names; only managed resources count.
			if strings.HasPrefix(name, "data.") {
				continue
			}
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
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("display_name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("placement_id"), knownvalue.StringRegexp(numericIDRegexp)),
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("placement_code"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("targeted_ad_units"), knownvalue.SetSizeExact(1)),
				},
				// The cross-resource set-membership assertion (that the placement
				// targets the created ad unit's id) has no clean statecheck analog,
				// so it stays a legacy Check. targeted_ad_units is a set: elements
				// are addressed by the "*" wildcard, not a stable numeric index.
				Check: resource.TestCheckTypeSetElemAttrPair(
					"admanager_placement.test", "targeted_ad_units.*",
					"admanager_ad_unit.target", "id"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Update the display name: must plan as an in-place update.
				Config: testAccPlacementConfig(code, root, adUnitName, updated),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("admanager_placement.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("display_name"), knownvalue.StringExact(updated)),
				},
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
