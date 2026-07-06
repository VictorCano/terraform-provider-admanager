# Custom targeting keys can be imported by their full resource name.
terraform import admanager_custom_targeting_key.example networks/123456/customTargetingKeys/321

# A bare numeric custom targeting key ID also works; it is expanded against the
# provider's configured network code.
terraform import admanager_custom_targeting_key.example 321
