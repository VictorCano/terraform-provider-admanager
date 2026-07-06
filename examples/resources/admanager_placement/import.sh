# Placements can be imported by their full resource name.
terraform import admanager_placement.example networks/123456/placements/789

# A bare numeric placement ID also works; it is expanded against the provider's
# configured network code.
terraform import admanager_placement.example 789
