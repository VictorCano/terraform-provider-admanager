# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the project is on a `0.x` version, breaking changes may occur between
minor releases. Any breaking change is documented in this file under the
release that introduces it, along with migration notes describing how to
update existing Terraform/OpenTofu configurations and state.

## [Unreleased]

### Added

- Initial provider scaffold.
- REST API client with rate limiting and retries.
- Provider configuration: `network_code`, `credentials`, `requests_per_second`, `retry_max_attempts`.
- `admanager_ad_unit` resource: full create, read, update (patch with field
  mask), and import support. `terraform destroy` archives the ad unit via
  `adUnits:batchArchive`; set `skip_archive_on_destroy = true` to drop it from
  state without touching Ad Manager. The `applied_labels`,
  `effective_applied_labels`, `applied_label_frequency_caps`, and
  `effective_label_frequency_caps` attributes are deferred to a later release.
- `admanager_placement` resource: full create, read, update (patch with field
  mask), and import support. `targeted_ad_units` takes full ad unit resource
  names, so managed ad units can be referenced directly (e.g.
  `admanager_ad_unit.example.id`). `terraform destroy` archives the placement
  via `placements:batchArchive`; set `skip_archive_on_destroy = true` to drop it
  from state without touching Ad Manager.
- `admanager_custom_targeting_key` resource: full create, read, update (patch
  with field mask), and import support. `ad_tag_name` is immutable (changing it
  forces replacement) and limited to 10 characters. `terraform destroy`
  **deactivates** the key via `customTargetingKeys:batchDeactivate` (its status
  becomes `INACTIVE`) — the API has no archive or hard delete for custom
  targeting keys; set `skip_archive_on_destroy = true` to drop it from state
  without touching Ad Manager.
- `admanager_custom_targeting_value` resource: full create, read, update, and
  import support. Custom targeting values are **read-only in the Ad Manager REST
  API**, so this resource reads over REST but performs **writes through the
  legacy SOAP `CustomTargetingService`** via a minimal internal compatibility
  shim (`internal/soap`, hand-built XML — no third-party SOAP library). The SOAP
  layer shares the REST client's authenticated HTTP client, OAuth token, and rate
  limiter, and requests the additional `https://www.googleapis.com/auth/dfp`
  scope alongside the REST scope. `custom_targeting_key`, `ad_tag_name` (max 40
  characters), and `match_type` are immutable (changing any forces replacement);
  `display_name` is the only field updated in place. `terraform destroy`
  **deactivates** the value via the SOAP `DeleteCustomTargetingValues` action
  (its status becomes `INACTIVE`) — values have no archive or hard delete; set
  `skip_archive_on_destroy = true` to drop it from state without touching Ad
  Manager. The SOAP layer is an implementation detail and will be removed
  transparently once the REST API ships value write endpoints. The pinned SOAP
  version (`v202605`) sunsets around May 2027 and must be bumped periodically;
  see the README.
