package provider

// Acceptance tests for admanager_ad_unit. These talk to a real Ad Manager test
// network and are skipped unless TF_ACC is set (the standard terraform-plugin-
// testing gate). They MUST run only against a network whose testNetwork flag is
// true (golden rule 3): testAccPreCheck aborts otherwise.
//
// Required environment for a run:
//   - ADMANAGER_TEST_NETWORK_CODE (or ADMANAGER_NETWORK_CODE): the test network.
//   - GOOGLE_CREDENTIALS_JSON (inline service account JSON) or
//     GOOGLE_APPLICATION_CREDENTIALS (path): credentials with access to it.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// numericIDRegexp matches a bare numeric GAM object ID. Every *_id attribute in
// these schemas is a types.String holding numericIDFromName's output, so ID
// assertions use knownvalue.StringRegexp with this pattern — NOT
// knownvalue.NumberFunc, which only matches json.Number state values and would
// fail at runtime with "expected json.Number ... got: string". Shared across the
// acceptance files in this package.
var numericIDRegexp = regexp.MustCompile(`^\d+$`)

// testAccProtoV6ProviderFactories serves the in-process provider to the test
// harness under the name "admanager".
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"admanager": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccNetworkCode resolves the test network code from the environment,
// preferring the acceptance-specific variable.
func testAccNetworkCode() string {
	if code := os.Getenv("ADMANAGER_TEST_NETWORK_CODE"); code != "" {
		return code
	}
	return os.Getenv("ADMANAGER_NETWORK_CODE")
}

// testAccClient builds a client from the environment for out-of-band checks
// (network guard, config generation). It mirrors how the provider authenticates.
func testAccClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(context.Background(), client.Config{
		NetworkCode: testAccNetworkCode(),
		Credentials: os.Getenv("GOOGLE_CREDENTIALS_JSON"),
	})
	if err != nil {
		t.Fatalf("building acceptance client: %v", err)
	}
	return c
}

// testAccPreCheck validates the environment and refuses to run against a
// production network. It is the enforcement point for golden rule 3.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if testAccNetworkCode() == "" {
		t.Fatal("ADMANAGER_TEST_NETWORK_CODE (or ADMANAGER_NETWORK_CODE) must be set for acceptance tests")
	}
	if os.Getenv("GOOGLE_CREDENTIALS_JSON") == "" && os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
		t.Fatal("GOOGLE_CREDENTIALS_JSON or GOOGLE_APPLICATION_CREDENTIALS must be set for acceptance tests")
	}
	net, err := testAccClient(t).GetNetwork(context.Background())
	if err != nil {
		t.Fatalf("acceptance pre-check: reading network: %v", err)
	}
	if !net.TestNetwork {
		t.Fatalf("refusing to run acceptance tests against non-test network %s; "+
			"acceptance tests must target a network whose testNetwork flag is true", net.NetworkCode)
	}
}

// testAccRootAdUnit returns the network's effective root ad unit resource name,
// under which acceptance fixtures are created.
func testAccRootAdUnit(t *testing.T) string {
	t.Helper()
	net, err := testAccClient(t).GetNetwork(context.Background())
	if err != nil {
		t.Fatalf("reading network root ad unit: %v", err)
	}
	if net.EffectiveRootAdUnit == "" {
		t.Fatal("network has no effectiveRootAdUnit")
	}
	return net.EffectiveRootAdUnit
}

func testAccAdUnitConfig(networkCode, parent, displayName string) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_ad_unit" "test" {
  parent_ad_unit = %[2]q
  display_name   = %[3]q

  sizes = [
    {
      width            = 300
      height           = 250
      size_type        = "PIXEL"
      environment_type = "BROWSER"
    },
    {
      width            = 728
      height           = 90
      size_type        = "PIXEL"
      environment_type = "BROWSER"
    },
  ]
}
`, networkCode, parent, displayName)
}

// testAccAdUnitConfigSkipArchive sets skip_archive_on_destroy to the given value.
// When true, destroy removes the resource from state without archiving it in Ad
// Manager. The value is a plain (non-RequiresReplace) provider-side attribute, so
// toggling it must plan as an in-place update, never a replace.
func testAccAdUnitConfigSkipArchive(networkCode, parent, displayName string, skip bool) string {
	return fmt.Sprintf(`
provider "admanager" {
  network_code = %[1]q
}

resource "admanager_ad_unit" "skip" {
  parent_ad_unit          = %[2]q
  display_name            = %[3]q
  skip_archive_on_destroy = %[4]t
}
`, networkCode, parent, displayName, skip)
}

// testAccCheckAdUnitArchived verifies that every ad unit in state reads back as
// ARCHIVED in Ad Manager after destroy — the documented default destroy
// semantics (archive, not hard delete). This runs against the real test network.
func testAccCheckAdUnitArchived(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		for name, rs := range s.RootModule().Resources {
			// Data sources share the type name; only managed resources count.
			if strings.HasPrefix(name, "data.") || rs.Type != "admanager_ad_unit" {
				continue
			}
			au, err := c.GetAdUnit(context.Background(), rs.Primary.ID)
			if err != nil {
				return fmt.Errorf("reading ad unit %s after destroy: %w", rs.Primary.ID, err)
			}
			if au.Status != "ARCHIVED" {
				return fmt.Errorf("ad unit %s status = %q after destroy, want ARCHIVED", rs.Primary.ID, au.Status)
			}
		}
		return nil
	}
}

// testAccCheckAdUnitStillActive verifies that skip_archive_on_destroy left the
// ad unit untouched (not ARCHIVED) in Ad Manager after destroy, then archives it
// best-effort so the test network does not accumulate live units.
func testAccCheckAdUnitStillActive(t *testing.T) resource.TestCheckFunc {
	t.Helper()
	return func(s *terraform.State) error {
		c := testAccClient(t)
		for name, rs := range s.RootModule().Resources {
			// Data sources share the type name; only managed resources count.
			if strings.HasPrefix(name, "data.") || rs.Type != "admanager_ad_unit" {
				continue
			}
			au, err := c.GetAdUnit(context.Background(), rs.Primary.ID)
			if err != nil {
				return fmt.Errorf("reading ad unit %s after skip-archive destroy: %w", rs.Primary.ID, err)
			}
			if au.Status == "ARCHIVED" {
				return fmt.Errorf("ad unit %s was archived despite skip_archive_on_destroy = true", rs.Primary.ID)
			}
			if archiveErr := c.ArchiveAdUnit(context.Background(), rs.Primary.ID); archiveErr != nil {
				t.Logf("cleanup: archiving %s left active by skip_archive_on_destroy: %v", rs.Primary.ID, archiveErr)
			}
		}
		return nil
	}
}

// testAccAdUnitBareIDImportFunc returns the resource's bare numeric ad_unit_id as
// the import ID, exercising normalizeAdUnitName's expansion of a bare id into a
// full resource name.
func testAccAdUnitBareIDImportFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		id := rs.Primary.Attributes["ad_unit_id"]
		if id == "" {
			return "", fmt.Errorf("resource %s has no ad_unit_id in state", resourceName)
		}
		return id, nil
	}
}

func TestAccAdUnitResource_basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	name := acctest.RandomWithPrefix("tf-acc-adunit")
	updated := name + "-updated"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// Default destroy must archive the ad unit (documented GAM semantics).
		CheckDestroy: testAccCheckAdUnitArchived(t),
		Steps: []resource.TestStep{
			{
				// Create. Statechecks are the primary assertions; ExpectEmptyPlan
				// after apply+refresh locks in zero post-create drift (this WOULD
				// fail without reconcileOmittedAppliedFields honestly carrying the
				// API-populated computed fields back into state).
				Config: testAccAdUnitConfig(code, root, name),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("display_name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("parent_ad_unit"), knownvalue.StringExact(root)),
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("ad_unit_id"), knownvalue.StringRegexp(numericIDRegexp)),
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("ad_unit_code"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("sizes"), knownvalue.ListSizeExact(2)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Update the display name. The plan must be an in-place update, not
				// a replace (a replace would archive+recreate a live ad unit).
				Config: testAccAdUnitConfig(code, root, updated),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("admanager_ad_unit.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("display_name"), knownvalue.StringExact(updated)),
				},
			},
			{
				// Import by full resource name; state must match.
				ResourceName:            "admanager_ad_unit.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"skip_archive_on_destroy"},
			},
			{
				// Import by bare numeric ad unit ID (exercises normalizeAdUnitName).
				ResourceName:            "admanager_ad_unit.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateIdFunc:       testAccAdUnitBareIDImportFunc("admanager_ad_unit.test"),
				ImportStateVerifyIgnore: []string{"skip_archive_on_destroy"},
			},
		},
	})
}

// TestAccAdUnitResource_skipArchiveOnDestroy verifies the destroy opt-out:
// with skip_archive_on_destroy = true, destroy removes the resource from state
// but leaves the ad unit untouched (still active) in Ad Manager.
func TestAccAdUnitResource_skipArchiveOnDestroy(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	name := acctest.RandomWithPrefix("tf-acc-adunit-skip")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckAdUnitStillActive(t),
		Steps: []resource.TestStep{
			{
				// Create with the opt-out off.
				Config: testAccAdUnitConfigSkipArchive(code, root, name, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_ad_unit.skip", tfjsonpath.New("display_name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue("admanager_ad_unit.skip", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
					statecheck.ExpectKnownValue("admanager_ad_unit.skip", tfjsonpath.New("skip_archive_on_destroy"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue("admanager_ad_unit.skip", tfjsonpath.New("ad_unit_id"), knownvalue.StringRegexp(numericIDRegexp)),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Toggle skip_archive_on_destroy on. This provider-side attribute
				// carries no RequiresReplace plan modifier, so the plan MUST be an
				// in-place update. Asserting ResourceActionUpdate guards against a
				// regression that made the toggle replace (archive+recreate) a live
				// ad unit. The final state has the opt-out ON, so CheckDestroy
				// (testAccCheckAdUnitStillActive) verifies the object is left intact.
				Config: testAccAdUnitConfigSkipArchive(code, root, name, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("admanager_ad_unit.skip", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_ad_unit.skip", tfjsonpath.New("skip_archive_on_destroy"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("admanager_ad_unit.skip", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
				},
			},
		},
	})
}
