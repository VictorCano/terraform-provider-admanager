# Custom targeting values can be imported by their full (flat) resource name.
terraform import admanager_custom_targeting_value.example networks/123456/customTargetingValues/555

# A bare numeric custom targeting value ID also works; it is expanded against the
# provider's configured network code.
terraform import admanager_custom_targeting_value.example 555
