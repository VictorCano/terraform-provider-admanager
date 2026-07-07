# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the project is on a `0.x` version, breaking changes may occur between
minor releases. Any breaking change is documented in this file under the
release that introduces it, along with migration notes describing how to
update existing Terraform/OpenTofu configurations and state.

## [Unreleased]

### Fixed

- `admanager_ad_unit`: `target_window` no longer fails apply with "Provider
  produced inconsistent result after apply … was cty.StringVal(\"BLANK\"), but
  now null" on networks whose REST responses omit `appliedTargetWindow` even
  though the write was accepted. When the response omits the applied field but
  its effective twin (`effectiveTargetWindow`) corroborates the known plan/state
  value, the resource now preserves that value; a genuine divergence still
  surfaces honestly as drift. The same effective-corroborated fallback is applied
  to `applied_adsense_enabled` (via `effectiveAdsenseEnabled`); `smart_size_mode`
  has no effective twin in the API and is intentionally left unchanged.
  ([#1](https://github.com/VictorCano/terraform-provider-admanager/issues/1))
- `admanager_ad_unit`: create and other failures now surface the Ad Manager
  `google.rpc` error details (`ErrorInfo.reason`/`domain`,
  `BadRequest.fieldViolations`, `LocalizedMessage`) instead of only the opaque
  top-level message ("An error occurred. Please try again later."). When a create
  fails with `400 INVALID_ARGUMENT` and `ad_unit_code` was set, the provider also
  looks up which unit (including archived ones) already holds that code and names
  it plus the recovery paths. A replace that would archive a unit still holding an
  `ad_unit_code` now emits a plan-time warning, and the docs describe the
  reservation behavior.
  ([#2](https://github.com/VictorCano/terraform-provider-admanager/issues/2))

### Added

- `admanager_custom_targeting_key` / `admanager_custom_targeting_value`:
  `ad_tag_name` now rejects the characters the API forbids
  (`" ' = ! + # * ~ ; ^ ( ) < > [ ]`, plus whitespace for keys) at plan time
  instead of failing the apply with a server-side
  `CUSTOM_TARGETING_ERROR_KEY_NAME_INVALID_CHARS` 400. The denylist was
  verified against the live API; configurations that applied successfully
  before are unaffected.
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
- `admanager_network` data source: reads the provider-configured network. Takes
  no arguments; exposes `network_code`, `display_name`, `time_zone`,
  `currency_code`, `secondary_currency_codes`, `effective_root_ad_unit`,
  `network_id`, `property_code`, and `test_network`. A common use is parenting a
  top-level `admanager_ad_unit` under `effective_root_ad_unit` without
  hardcoding the root's resource name.
- `admanager_ad_unit` data source: looks up a single ad unit by exactly one of
  `ad_unit_id` (a bare numeric ID or a full resource name) or `ad_unit_code`
  (exact match via a server-side list filter). Exposes the same attribute set as
  the `admanager_ad_unit` resource, minus `skip_archive_on_destroy`.
- `admanager_ad_units` data source: lists ad units in the network, optionally
  narrowed by an [AIP-160 `filter`](https://developers.google.com/ad-manager/api/beta/filters)
  passed straight through to the API (wildcards via `*`; the `like` operator is
  not supported). Returns `ad_units`, a list of `{id, ad_unit_id,
  parent_ad_unit, display_name, ad_unit_code, status}`. Results are paginated
  transparently across all pages; a too-broad filter that exceeds a 100-page
  safety cap errors rather than truncating silently.
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
