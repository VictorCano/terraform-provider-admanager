# List every ad unit in the network.
data "admanager_ad_units" "all" {}

# Or narrow with a filter. The filter is an AIP-160 expression passed straight
# through to the API. See the syntax at
# https://developers.google.com/ad-manager/api/beta/filters
#
# Filterable fields include adUnitCode, displayName, parentAdUnit, status,
# hasChildren, and explicitlyTargeted. Use * for wildcards; the `like` operator
# is NOT supported.

# Direct children of the network's root ad unit that are currently active.
data "admanager_network" "current" {}

data "admanager_ad_units" "active_children" {
  filter = "parentAdUnit = \"${data.admanager_network.current.effective_root_ad_unit}\" AND status = \"ACTIVE\""
}

# Wildcard match on display name.
data "admanager_ad_units" "homepages" {
  filter = "displayName = \"Homepage*\""
}

output "active_child_ids" {
  value = [for u in data.admanager_ad_units.active_children.ad_units : u.id]
}
