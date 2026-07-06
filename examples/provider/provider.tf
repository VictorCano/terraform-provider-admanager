provider "admanager" {
  network_code = "123456"           # or set ADMANAGER_NETWORK_CODE
  credentials  = "/path/to/sa.json" # or use Application Default Credentials

  # Optional: Ad Manager API quotas are low; defaults are conservative.
  requests_per_second = 2
  retry_max_attempts  = 5
}
