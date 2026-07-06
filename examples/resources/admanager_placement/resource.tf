# A placement groups ad units so line items can target them together.
#
# targeted_ad_units holds full ad unit resource names. Reference managed ad
# units directly through their `id` so Terraform orders the operations and keeps
# the references in sync.

resource "admanager_ad_unit" "homepage" {
  parent_ad_unit = "networks/123456/adUnits/1" # the network's root ad unit
  display_name   = "Homepage"
}

resource "admanager_ad_unit" "sports" {
  parent_ad_unit = "networks/123456/adUnits/1"
  display_name   = "Sports"
}

resource "admanager_placement" "run_of_site" {
  display_name = "Run of Site"
  description  = "All top-level inventory"

  targeted_ad_units = [
    admanager_ad_unit.homepage.id,
    admanager_ad_unit.sports.id,
  ]
}
