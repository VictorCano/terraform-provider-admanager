package provider

// Live out-of-band drift and replace-path acceptance coverage (P1-1, P1-3).
// These talk to the real Ad Manager test network and are skipped unless TF_ACC
// is set; testAccPreCheck enforces the testNetwork guard (golden rule 3).
//
// # Out-of-band drift: the asserted, documented behavior
//
// Every drift test archives/deactivates the object OUT OF BAND (through the same
// client the provider uses) and then re-plans the UNCHANGED config.
//
// For the ad unit, placement, and value, status is the ONLY thing the mutation
// changes and it is a Computed-only attribute (no config counterpart to diff
// against), so Terraform ABSORBS the archived/inactive status into state on
// refresh and produces an EMPTY plan — it is NOT surfaced as an actionable diff
// and is NOT self-healed. That is the honest current behavior (P1-1 design (a)),
// and these tests make it an asserted, documented fact. (Design (b) — mapping an
// out-of-band ARCHIVED read to RemoveResource so archive plans as recreate —
// remains an open spec decision and is deliberately NOT implemented here.)
//
// The custom targeting KEY is the documented exception: deactivating a key also
// resets reportable_type (ON -> OFF), a CONFIGURED attribute, so its out-of-band
// drift is actionable and un-healable rather than absorbed. Its drift step
// asserts that honestly (RefreshState + an enforcing plancheck.ExpectNonEmptyPlan);
// see the step comment in TestAccCustomTargetingKeyResource_replaceAndDrift.
//
// Form choice: a v1.16 RefreshState step runs the legacy Check (not the modern
// ConfigStateChecks) and enforces an empty post-refresh plan itself. To use the
// modern statecheck/plancheck idiom that the rest of this suite uses, the drift
// step is instead a same-Config step whose PreConfig performs the out-of-band
// mutation, with ConfigStateChecks asserting the absorbed status and
// ConfigPlanChecks.PostApplyPostRefresh asserting ExpectEmptyPlan.
//
// # ad_unit replace is intentionally absent from the always-run suite
//
// Replacing an ad unit archives the old one, which keeps its ad_unit_code
// reserved forever; the replacement create reuses that same (auto-assigned,
// UseStateForUnknown-retained) code and fails with 400 INVALID_ARGUMENT — this
// is documented on the resource itself (ad_unit_resource.go, the ad_unit_code
// attribute) and encoded in adUnitReplaceReservesCode. A "successful" ad_unit
// replace is therefore impossible without permanently reserving a code, so it is
// NOT run in the weekly cron. ad_unit replace behavior is covered by the
// in-process ModifyPlan warning test (P0-8) and the manual-only, build-tagged
// reserved-code collision e2e (ad_unit_reserved_code_acc_test.go). Placement has
// no RequiresReplace attribute at all. Only custom targeting keys and values
// have a cron-safe replace path (change an immutable ad_tag_name to a fresh
// random value — the new object claims an unused name, so no collision).

import (
	"context"
	"fmt"
	"os"
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

// testAccCaptureID stores the primary ID of the named resource into dst when the
// step's checks run. A later step's PreConfig closure reads dst to mutate that
// exact object out of band. PreConfig takes no state argument, so capturing the
// ID in the prior step is the only way to hand it the target.
func testAccCaptureID(resourceName string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %s not found in state", resourceName)
		}
		if rs.Primary.ID == "" {
			return fmt.Errorf("resource %s has no primary ID in state", resourceName)
		}
		*dst = rs.Primary.ID
		return nil
	}
}

// testAccCaptureAttr stores the named attribute of the named resource into dst.
func testAccCaptureAttr(resourceName, attr string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %s not found in state", resourceName)
		}
		v, ok := rs.Primary.Attributes[attr]
		if !ok {
			return fmt.Errorf("resource %s has no attribute %q in state", resourceName, attr)
		}
		*dst = v
		return nil
	}
}

// testAccDeactivateValueOOB deactivates a custom targeting value out of band via
// the SOAP shim, mirroring exactly how the value resource's Delete deactivates
// it. It resolves the numeric key and value ids from their resource names.
func testAccDeactivateValueOOB(t *testing.T, keyName, valueName string) {
	t.Helper()
	c := testAccClient(t)
	sc := soap.NewClient(soap.Config{
		HTTPClient:      c.HTTPClient(),
		Limiter:         c.Limiter(),
		NetworkCode:     c.NetworkCode(),
		ApplicationName: c.UserAgent(),
	})
	keyID, err := soap.KeyIDFromResourceName(keyName)
	if err != nil {
		t.Fatalf("out-of-band deactivate: parsing key id from %q: %v", keyName, err)
	}
	valueID, err := soap.ValueIDFromResourceName(valueName)
	if err != nil {
		t.Fatalf("out-of-band deactivate: parsing value id from %q: %v", valueName, err)
	}
	if _, err := sc.DeleteCustomTargetingValue(context.Background(), keyID, valueID); err != nil {
		t.Fatalf("out-of-band deactivate of value %q: %v", valueName, err)
	}
}

// TestAccAdUnitResource_outOfBandArchive asserts the absorbed-drift behavior for
// ad units: an out-of-band archive refreshes status to ARCHIVED and produces an
// empty plan (computed-only drift, absorbed — see the file header). CheckDestroy
// then archives again, which must tolerate the already-archived unit (the
// verified-tolerance Delete path).
func TestAccAdUnitResource_outOfBandArchive(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	name := acctest.RandomWithPrefix("tf-acc-adunit-drift")
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckAdUnitArchived(t),
		Steps: []resource.TestStep{
			{
				Config: testAccAdUnitConfig(code, root, name),
				Check:  testAccCaptureID("admanager_ad_unit.test", &id),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
				},
			},
			{
				// Archive out of band, then re-plan the unchanged config. The
				// refreshed status is ARCHIVED and the plan is empty: computed-only
				// drift is absorbed, never surfaced as an actionable diff.
				PreConfig: func() {
					if err := testAccClient(t).ArchiveAdUnit(context.Background(), id); err != nil {
						t.Fatalf("out-of-band archive of ad unit %q: %v", id, err)
					}
				},
				Config: testAccAdUnitConfig(code, root, name),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_ad_unit.test", tfjsonpath.New("status"), knownvalue.StringExact("ARCHIVED")),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccPlacementResource_outOfBandArchive is the placement equivalent of the
// ad-unit drift test. Placement has no RequiresReplace attribute, so there is no
// replace path to cover — drift only.
func TestAccPlacementResource_outOfBandArchive(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	root := testAccRootAdUnit(t)
	adUnitName := acctest.RandomWithPrefix("tf-acc-placement-drift-au")
	name := acctest.RandomWithPrefix("tf-acc-placement-drift")
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckPlacementArchived(t),
		Steps: []resource.TestStep{
			{
				Config: testAccPlacementConfig(code, root, adUnitName, name),
				Check:  testAccCaptureID("admanager_placement.test", &id),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
				},
			},
			{
				PreConfig: func() {
					if err := testAccClient(t).ArchivePlacement(context.Background(), id); err != nil {
						t.Fatalf("out-of-band archive of placement %q: %v", id, err)
					}
				},
				Config: testAccPlacementConfig(code, root, adUnitName, name),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_placement.test", tfjsonpath.New("status"), knownvalue.StringExact("ARCHIVED")),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccCustomTargetingKeyResource_replaceAndDrift bundles the two live paths a
// key needs beyond its basic test: a replace (change the immutable ad_tag_name
// to a fresh random value — the new key claims an unused name, so no collision)
// and out-of-band deactivation absorbed as INACTIVE with an empty plan. Reusing
// one create/destroy cycle keeps the shared-network runtime budget down.
func TestAccCustomTargetingKeyResource_replaceAndDrift(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	tag1 := testAccCustomTargetingKeyAdTagName(t)
	tag2 := testAccCustomTargetingKeyAdTagName(t)
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCustomTargetingKeyInactive(t),
		Steps: []resource.TestStep{
			{
				Config: testAccCustomTargetingKeyConfig(code, tag1, "Genre"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("ad_tag_name"), knownvalue.StringExact(tag1)),
				},
			},
			{
				// Change the immutable ad_tag_name: must plan as a full replace.
				Config: testAccCustomTargetingKeyConfig(code, tag2, "Genre"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("admanager_custom_targeting_key.test", plancheck.ResourceActionReplace)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCaptureID("admanager_custom_targeting_key.test", &id),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_key.test", tfjsonpath.New("ad_tag_name"), knownvalue.StringExact(tag2)),
				},
			},
			{
				// Out-of-band deactivation of a KEY is NOT cleanly absorbed the way
				// an ad unit / value archive is. Empirically, deactivating a key
				// flips BOTH status (-> INACTIVE) AND reportable_type (ON -> OFF):
				// deactivation resets the key's reportability. reportable_type is a
				// CONFIGURED (required) attribute, so unlike a pure computed-status
				// change this surfaces as ACTIONABLE drift — a refresh shows
				// reportable_type drifted to OFF and Terraform plans to restore it to
				// ON. That heal is NOT applyable (patching an inactive key fails
				// 400 CUSTOM_TARGETING_ERROR_KEY_NOT_FOUND), so this step only
				// RefreshState and asserts the drifted values with ExpectNonEmptyPlan,
				// making the non-absorbed behavior an asserted, documented fact.
				PreConfig: func() {
					if err := testAccClient(t).DeactivateCustomTargetingKey(context.Background(), id); err != nil {
						t.Fatalf("out-of-band deactivate of key %q: %v", id, err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				// The plain ExpectNonEmptyPlan bool above is only a one-directional
				// guard on a RefreshState step: v1.16's refresh executor errors when
				// the post-refresh plan is unexpectedly non-empty, but it has NO branch
				// that fails when ExpectNonEmptyPlan is true yet the plan is empty
				// (unlike the Config-mode executor). So the bool alone cannot detect a
				// regression to absorbed/self-healed drift. plancheck.ExpectNonEmptyPlan()
				// genuinely asserts at least one non-NoOp change, making the
				// non-absorbed behavior an enforced fact rather than a documented claim.
				RefreshPlanChecks: resource.RefreshPlanChecks{
					PostRefresh: []plancheck.PlanCheck{plancheck.ExpectNonEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("admanager_custom_targeting_key.test", "status", "INACTIVE"),
					resource.TestCheckResourceAttr("admanager_custom_targeting_key.test", "reportable_type", "OFF"),
				),
			},
		},
	})
}

// TestAccCustomTargetingValueResource_replaceAndDrift is the value equivalent:
// replace via a fresh ad_tag_name (SOAP deactivate old + create new) and
// out-of-band SOAP deactivation absorbed as INACTIVE with an empty plan. This is
// the highest exit-risk resource (the SOAP shim), so both paths are exercised
// end to end against the live network.
func TestAccCustomTargetingValueResource_replaceAndDrift(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
	code := testAccNetworkCode()
	keyAdTag := testAccCustomTargetingKeyAdTagName(t)
	val1 := testAccCustomTargetingValueName(t)
	val2 := testAccCustomTargetingValueName(t)
	var valueName, keyName string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			testAccCheckCustomTargetingValueInactive(t),
			testAccCheckCustomTargetingKeyInactive(t),
		),
		Steps: []resource.TestStep{
			{
				Config: testAccCustomTargetingValueConfig(code, keyAdTag, val1, "Honda"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_value.test", tfjsonpath.New("ad_tag_name"), knownvalue.StringExact(val1)),
				},
			},
			{
				// Change the immutable ad_tag_name: the value must be replaced. The
				// parent key is unchanged, so only the value replaces.
				Config: testAccCustomTargetingValueConfig(code, keyAdTag, val2, "Honda"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("admanager_custom_targeting_value.test", plancheck.ResourceActionReplace)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCaptureID("admanager_custom_targeting_value.test", &valueName),
					testAccCaptureAttr("admanager_custom_targeting_value.test", "custom_targeting_key", &keyName),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_value.test", tfjsonpath.New("ad_tag_name"), knownvalue.StringExact(val2)),
				},
			},
			{
				// Deactivate the value out of band (SOAP): status refreshes to
				// INACTIVE and the plan is empty (absorbed computed-only drift).
				PreConfig: func() { testAccDeactivateValueOOB(t, keyName, valueName) },
				Config:    testAccCustomTargetingValueConfig(code, keyAdTag, val2, "Honda"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("admanager_custom_targeting_value.test", tfjsonpath.New("status"), knownvalue.StringExact("INACTIVE")),
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
