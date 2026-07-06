# Reads the network the provider is configured for. Takes no arguments.
data "admanager_network" "current" {}

# A common use: parent a top-level ad unit under the network's root ad unit
# without hardcoding the root's resource name.
resource "admanager_ad_unit" "homepage" {
  parent_ad_unit = data.admanager_network.current.effective_root_ad_unit
  display_name   = "Homepage"
}

output "network_currency" {
  value = data.admanager_network.current.currency_code
}
