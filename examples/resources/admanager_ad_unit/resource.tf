# A web ad unit placed directly under the network's root ad unit, serving a
# 300x250 medium rectangle and a 728x90 leaderboard in the browser environment.
#
# The parent must be an existing ad unit's resource name. For a top-level unit,
# use the network's effective root ad unit, available as
# data.admanager_network.current.effective_root_ad_unit (or from the Ad
# Manager UI).

resource "admanager_ad_unit" "homepage" {
  parent_ad_unit = "networks/123456/adUnits/1" # the network's root ad unit
  display_name   = "Homepage"
  description    = "Homepage inventory"

  # Optional, immutable ad serving code. If omitted, Ad Manager assigns one.
  ad_unit_code = "homepage"

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
