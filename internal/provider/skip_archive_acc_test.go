package provider

// skip_archive_on_destroy behavior coverage for placement, custom targeting key,
// and custom targeting value (ad_unit already has TestAccAdUnitResource_skipArchiveOnDestroy).
// Each test creates the object with skip_archive_on_destroy = true and destroys
// it; the dedicated CheckDestroy asserts the object was left UNTOUCHED (still
// active) in Ad Manager rather than archived/deactivated, then best-effort
// archives/deactivates it so the shared test network does not accumulate live
// objects. These mirror testAccCheckAdUnitStillActive.
//
// Skipped unless TF_ACC is set; testAccPreCheck enforces the testNetwork guard.

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

	"github.com/VictorCano/terraform-provider-admanager/internal/soap"
)

func testAccPlacementConfigSkipArchive(networkCode, parent, adUnitName, placementName string, skip bool) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_ad_unit" "au" {
  parent_ad_unit = %[2]q
  display_name   = %[3]q
}

resource "admanager_placement" "skip" {
  display_name            = %[4]q
  targeted_ad_units       = [admanager_ad_unit.au.id]
  skip_archive_on_destroy = %[5]t
}
`, networkCode, parent, adUnitName, placementName, skip)
}

func testAccCustomTargetingKeyConfigSkipArchive(networkCode, adTagName, displayName string, skip bool) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_custom_targeting_key" "skip" {
  ad_tag_name             = %[2]q
  display_name            = %[3]q
  type                    = "FREEFORM"
  reportable_type         = "ON"
  skip_archive_on_destroy = %[4]t
}
`, networkCode, adTagName, displayName, skip)
}

func testAccCustomTargetingValueConfigSkipArchive(networkCode, keyAdTag, valueName, displayName string, skip bool) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_custom_targeting_key" "key" {
  ad_tag_name     = %[2]q
  type            = "PREDEFINED"
  reportable_type = "ON"
  # The parent key also opts out of destroy so it stays ACTIVE through
  # CheckDestroy: Ad Manager refuses to deactivate a value whose key is already
  # inactive (KEY_STATUS_NOT_ACTIVE), so the cleanup must deactivate the value
  # first (key still active) and only then the key.
  skip_archive_on_destroy = %[5]t
}

resource "admanager_custom_targeting_value" "skip" {
  custom_targeting_key    = admanager_custom_targeting_key.key.id
  ad_tag_name             = %[3]q
  display_name            = %[4]q
  match_type              = "EXACT"
  skip_archive_on_destroy = %[5]t
}
`, networkCode, keyAdTag, valueName, displayName, skip)
}

// testAccCheckPlacementStillActive verifies skip_archive_on_destroy left the
// placement untouched (not ARCHIVED) after destroy, then archives it best-effort.
func testAccCheckPlacementStillActive(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		for name, rs := range s.RootModule().Resources {
			if strings.HasPrefix(name, "data.") || rs.Type != "admanager_placement" {
				continue
			}
			p, err := c.GetPlacement(context.Background(), rs.Primary.ID)
			if err != nil {
				return fmt.Errorf("reading placement %s after skip-archive destroy: %w", rs.Primary.ID, err)
			}
			if p.Status == "ARCHIVED" {
				return fmt.Errorf("placement %s was archived despite skip_archive_on_destroy = true", rs.Primary.ID)
			}
			if archiveErr := c.ArchivePlacement(context.Background(), rs.Primary.ID); archiveErr != nil {
				t.Logf("cleanup: archiving placement %s left active by skip_archive_on_destroy: %v", rs.Primary.ID, archiveErr)
			}
		}
		return nil
	}
}

// testAccCheckCustomTargetingKeyStillActive verifies skip_archive_on_destroy left
// the key ACTIVE after destroy, then deactivates it best-effort.
func testAccCheckCustomTargetingKeyStillActive(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		for name, rs := range s.RootModule().Resources {
			if strings.HasPrefix(name, "data.") || rs.Type != "admanager_custom_targeting_key" {
				continue
			}
			k, err := c.GetCustomTargetingKey(context.Background(), rs.Primary.ID)
			if err != nil {
				return fmt.Errorf("reading custom targeting key %s after skip-archive destroy: %w", rs.Primary.ID, err)
			}
			if k.Status != "ACTIVE" {
				return fmt.Errorf("custom targeting key %s status = %q after skip-archive destroy, want ACTIVE (left untouched)", rs.Primary.ID, k.Status)
			}
			if deErr := c.DeactivateCustomTargetingKey(context.Background(), rs.Primary.ID); deErr != nil {
				t.Logf("cleanup: deactivating key %s left active by skip_archive_on_destroy: %v", rs.Primary.ID, deErr)
			}
		}
		return nil
	}
}

// testAccCheckCustomTargetingValueStillActive verifies skip_archive_on_destroy
// left the value ACTIVE after destroy, then deactivates it best-effort via SOAP.
func testAccCheckCustomTargetingValueStillActive(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		sc := soap.NewClient(soap.Config{
			HTTPClient:      c.HTTPClient(),
			Limiter:         c.Limiter(),
			NetworkCode:     c.NetworkCode(),
			ApplicationName: c.UserAgent(),
		})
		for name, rs := range s.RootModule().Resources {
			if strings.HasPrefix(name, "data.") || rs.Type != "admanager_custom_targeting_value" {
				continue
			}
			v, err := c.GetCustomTargetingValue(context.Background(), rs.Primary.ID)
			if err != nil {
				return fmt.Errorf("reading custom targeting value %s after skip-archive destroy: %w", rs.Primary.ID, err)
			}
			if v.Status != "ACTIVE" {
				return fmt.Errorf("custom targeting value %s status = %q after skip-archive destroy, want ACTIVE (left untouched)", rs.Primary.ID, v.Status)
			}
			keyID, keyErr := soap.KeyIDFromResourceName(v.CustomTargetingKey)
			valueID, valErr := soap.ValueIDFromResourceName(v.Name)
			if keyErr != nil || valErr != nil {
				t.Logf("cleanup: parsing ids for value %s: keyErr=%v valErr=%v", rs.Primary.ID, keyErr, valErr)
				continue
			}
			// Deactivate the value first (its parent key is still ACTIVE thanks to
			// the key's own skip opt-out), then deactivate the parent key. The
			// reverse order fails with KEY_STATUS_NOT_ACTIVE.
			if _, deErr := sc.DeleteCustomTargetingValue(context.Background(), keyID, valueID); deErr != nil {
				t.Logf("cleanup: deactivating value %s left active by skip_archive_on_destroy: %v", rs.Primary.ID, deErr)
			}
			if deErr := c.DeactivateCustomTargetingKey(context.Background(), v.CustomTargetingKey); deErr != nil {
				t.Logf("cleanup: deactivating parent key %s: %v", v.CustomTargetingKey, deErr)
			}
		}
		return nil
	}
}

func TestAccPlacementResource_skipArchiveOnDestroy(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	adUnitName := acctest.RandomWithPrefix("tf-acc-placement-skip-au")
	name := acctest.RandomWithPrefix("tf-acc-placement-skip")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPlacementStillActive(t),
		Steps: []resource.TestStep{
			{
				Config: testAccPlacementConfigSkipArchive(code, root, adUnitName, name, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_placement.skip", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
					statecheck.ExpectKnownValue("admanager_placement.skip", tfjsonpath.New("skip_archive_on_destroy"), knownvalue.Bool(true)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccCustomTargetingKeyResource_skipArchiveOnDestroy(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	adTagName := testAccCustomTargetingKeyAdTagName(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCustomTargetingKeyStillActive(t),
		Steps: []resource.TestStep{
			{
				Config: testAccCustomTargetingKeyConfigSkipArchive(code, adTagName, "Genre", true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.skip", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.skip", tfjsonpath.New("skip_archive_on_destroy"), knownvalue.Bool(true)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccCustomTargetingValueResource_skipArchiveOnDestroy(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	keyAdTag := testAccCustomTargetingKeyAdTagName(t)
	valueName := testAccCustomTargetingValueName(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// Only the value opts out; the parent key is deactivated normally on
		// destroy, so the check asserts just the value was left ACTIVE.
		CheckDestroy: testAccCheckCustomTargetingValueStillActive(t),
		Steps: []resource.TestStep{
			{
				Config: testAccCustomTargetingValueConfigSkipArchive(code, keyAdTag, valueName, "Honda", true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_value.skip", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
					statecheck.ExpectKnownValue("admanager_custom_targeting_value.skip", tfjsonpath.New("skip_archive_on_destroy"), knownvalue.Bool(true)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
