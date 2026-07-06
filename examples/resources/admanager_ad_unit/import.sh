# Ad units can be imported by their full resource name.
terraform import admanager_ad_unit.example networks/123456/adUnits/456

# A bare numeric ad unit ID also works; it is expanded against the provider's
# configured network code.
terraform import admanager_ad_unit.example 456
