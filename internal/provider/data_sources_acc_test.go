package provider

// Acceptance tests for the admanager_network, admanager_ad_unit, and
// admanager_ad_units data sources. They talk to a real Ad Manager test network
// and are skipped unless TF_ACC is set. testAccPreCheck aborts against any
// network whose testNetwork flag is not true (golden rule 3). The shared
// helpers (testAccPreCheck, testAccNetworkCode, testAccRootAdUnit,
// testAccProtoV6ProviderFactories, testAccCheckAdUnitArchived) live in
// ad_unit_resource_acc_test.go.

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccNetworkDataSourceConfig(networkCode string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

data "admanager_network" "current" {}
`, networkCode)
}

func TestAccNetworkDataSource_basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()

	// acc-test-guard:no-checkdestroy — reads a data source only; creates nothing
	// to archive/deactivate on destroy.
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccNetworkDataSourceConfig(code),
				ConfigStateChecks: []statecheck.StateCheck{
					// The pre-check already guarantees a test network; assert the data
					// source reports it faithfully.
					statecheck.ExpectKnownValue("data.admanager_network.current", tfjsonpath.New("test_network"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("data.admanager_network.current", tfjsonpath.New("network_code"), knownvalue.StringExact(code)),
					statecheck.ExpectKnownValue("data.admanager_network.current", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("data.admanager_network.current", tfjsonpath.New("display_name"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("data.admanager_network.current", tfjsonpath.New("effective_root_ad_unit"), knownvalue.NotNull()),
				},
			},
		},
	})
}

// testAccAdUnitDataSourceConfig creates a child ad unit under the network root,
// then reads the root by id and the child by its (computed) ad_unit_code. The
// by-code data source references the resource, so Terraform reads it only after
// the child exists.
func testAccAdUnitDataSourceConfig(networkCode, root, displayName, code string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_ad_unit" "child" {
  parent_ad_unit = %[2]q
  display_name   = %[3]q
  ad_unit_code   = %[4]q
}

data "admanager_ad_unit" "root" {
  ad_unit_id = %[2]q
}

data "admanager_ad_unit" "by_code" {
  ad_unit_code = admanager_ad_unit.child.ad_unit_code
}
`, networkCode, root, displayName, code)
}

func TestAccAdUnitDataSource_byIDAndCode(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	name := acctest.RandomWithPrefix("tf-acc-ds-adunit")
	adUnitCode := acctest.RandomWithPrefix("tf_acc_ds_code")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckAdUnitArchived(t),
		Steps: []resource.TestStep{
			{
				Config: testAccAdUnitDataSourceConfig(code, root, name, adUnitCode),
				ConfigStateChecks: []statecheck.StateCheck{
					// Lookup by id resolves the network's effective root ad unit.
					statecheck.ExpectKnownValue("data.admanager_ad_unit.root", tfjsonpath.New("id"), knownvalue.StringExact(root)),
					statecheck.ExpectKnownValue("data.admanager_ad_unit.root", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
					// Lookup by code resolves the created child; its computed
					// attributes match the managed resource.
					statecheck.ExpectKnownValue("data.admanager_ad_unit.by_code", tfjsonpath.New("display_name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue("data.admanager_ad_unit.by_code", tfjsonpath.New("ad_unit_code"), knownvalue.StringExact(adUnitCode)),
				},
				// The by_code data source's id must match the managed child's id; the
				// cross-resource pairing has no clean statecheck analog.
				Check: resource.TestCheckResourceAttrPair(
					"data.admanager_ad_unit.by_code", "id",
					"admanager_ad_unit.child", "id"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// testAccCheckAdUnitsListContains asserts the ad_units list on dsName contains
// the id of the ad unit managed by childResource.
func testAccCheckAdUnitsListContains(dsName, childResource string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		child, ok := s.RootModule().Resources[childResource]
		if !ok {
			return fmt.Errorf("resource %s not found in state", childResource)
		}
		wantID := child.Primary.ID

		ds, ok := s.RootModule().Resources[dsName]
		if !ok {
			return fmt.Errorf("data source %s not found in state", dsName)
		}
		count, err := strconv.Atoi(ds.Primary.Attributes["ad_units.#"])
		if err != nil {
			return fmt.Errorf("reading ad_units.# on %s: %w", dsName, err)
		}
		for i := 0; i < count; i++ {
			if ds.Primary.Attributes[fmt.Sprintf("ad_units.%d.id", i)] == wantID {
				return nil
			}
		}
		return fmt.Errorf("ad unit %s not found among the %d ad_units listed by %s", wantID, count, dsName)
	}
}

// testAccAdUnitsDataSourceConfig creates a child under the root and lists the
// root's children. depends_on defers the read until after the child exists.
func testAccAdUnitsDataSourceConfig(networkCode, root, displayName string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_ad_unit" "child" {
  parent_ad_unit = %[2]q
  display_name   = %[3]q
}

data "admanager_ad_units" "children" {
  filter     = "parentAdUnit = \"%[2]s\""
  depends_on = [admanager_ad_unit.child]
}
`, networkCode, root, displayName)
}

func TestAccAdUnitsDataSource_childrenOfRoot(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	name := acctest.RandomWithPrefix("tf-acc-ds-adunits")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckAdUnitArchived(t),
		Steps: []resource.TestStep{
			{
				Config: testAccAdUnitsDataSourceConfig(code, root, name),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_ad_unit.child", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
				},
				// The created child must appear among the root's listed children; the
				// list-membership assertion has no clean statecheck analog.
				Check: testAccCheckAdUnitsListContains("data.admanager_ad_units.children", "admanager_ad_unit.child"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
