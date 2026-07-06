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
