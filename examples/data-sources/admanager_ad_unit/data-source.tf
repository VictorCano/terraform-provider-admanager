# Look up a single ad unit. Set EXACTLY ONE of ad_unit_id or ad_unit_code.

# By ID: a bare numeric ad unit ID (expanded against the configured network)...
data "admanager_ad_unit" "by_id" {
  ad_unit_id = "123456789"
}

# ...or a full resource name.
data "admanager_ad_unit" "by_name" {
  ad_unit_id = "networks/123456/adUnits/123456789"
}

# By ad serving code (exact match).
data "admanager_ad_unit" "by_code" {
  ad_unit_code = "homepage_leaderboard"
}

output "homepage_status" {
  value = data.admanager_ad_unit.by_code.status
}
