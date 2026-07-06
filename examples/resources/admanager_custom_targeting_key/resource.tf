# A custom targeting key defines one dimension of key-value targeting. Its
# values are managed separately (see admanager_custom_targeting_value).
#
# ad_tag_name is the key used in ad tags: at most 10 characters and immutable
# (changing it forces replacement). type is PREDEFINED (a fixed set of values)
# or FREEFORM.

resource "admanager_custom_targeting_key" "genre" {
  ad_tag_name     = "genre"
  display_name    = "Content genre"
  type            = "FREEFORM"
  reportable_type = "ON"
}
