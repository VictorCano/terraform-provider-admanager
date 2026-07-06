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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccNetworkDataSourceConfig(code),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The pre-check already guarantees a test network; assert the data
					// source reports it faithfully.
					resource.TestCheckResourceAttr("data.admanager_network.current", "test_network", "true"),
					resource.TestCheckResourceAttr("data.admanager_network.current", "network_code", code),
					resource.TestCheckResourceAttrSet("data.admanager_network.current", "id"),
					resource.TestCheckResourceAttrSet("data.admanager_network.current", "display_name"),
					resource.TestCheckResourceAttrSet("data.admanager_network.current", "effective_root_ad_unit"),
				),
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
				Check: resource.ComposeAggregateTestCheckFunc(
					// Lookup by id resolves the network's effective root ad unit.
					resource.TestCheckResourceAttr("data.admanager_ad_unit.root", "id", root),
					resource.TestCheckResourceAttr("data.admanager_ad_unit.root", "status", "ACTIVE"),
					// Lookup by code resolves the created child; its computed
					// attributes match the managed resource.
					resource.TestCheckResourceAttr("data.admanager_ad_unit.by_code", "display_name", name),
					resource.TestCheckResourceAttr("data.admanager_ad_unit.by_code", "ad_unit_code", adUnitCode),
					resource.TestCheckResourceAttrPair(
						"data.admanager_ad_unit.by_code", "id",
						"admanager_ad_unit.child", "id"),
				),
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
				Check: resource.ComposeAggregateTestCheckFunc(
					// The created child must appear among the root's children.
					testAccCheckAdUnitsListContains("data.admanager_ad_units.children", "admanager_ad_unit.child"),
				),
			},
		},
	})
}
