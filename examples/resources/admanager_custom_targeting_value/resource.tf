# A custom targeting value is the right-hand side of key-value targeting: for
# the key "car", a value like "honda". Values belong to a custom targeting key.
#
# NOTE: values are read-only in the Ad Manager REST API, so this provider writes
# them through the legacy SOAP CustomTargetingService. This is invisible in the
# configuration — the resource behaves like every other one. See the provider
# README section "Custom targeting values use a SOAP compatibility layer".
#
# ad_tag_name is the value string (at most 40 characters, immutable) and
# match_type is how it matches ad requests (EXACT, BROAD, PREFIX, BROAD_PREFIX,
# SUFFIX, CONTAINS) — both immutable, so changing them forces replacement. Only
# display_name can be updated in place.

resource "admanager_custom_targeting_key" "car" {
  ad_tag_name     = "car"
  display_name    = "Car make"
  type            = "PREDEFINED"
  reportable_type = "ON"
}

resource "admanager_custom_targeting_value" "honda" {
  custom_targeting_key = admanager_custom_targeting_key.car.id
  ad_tag_name          = "honda"
  display_name         = "Honda"
  match_type           = "EXACT"
}
