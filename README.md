# terraform-provider-admanager

The first [Terraform](https://developer.hashicorp.com/terraform) / [OpenTofu](https://opentofu.org) provider for **Google Ad Manager (GAM)**. Manage your ad inventory — ad units, placements, and custom targeting — as code, with `plan`/`apply`, state, drift detection, and `import`, built on the official [Ad Manager REST API](https://developers.google.com/ad-manager/api/beta) (`admanager.googleapis.com`, v1).

> **Status: pre-release, working toward `v0.1.0`.**
> The provider is under active development and **not yet published to the Terraform Registry** (that ships with `v0.1.0` — see the [Quickstart](#quickstart) caveat). The four resources and three read-only data sources below are all implemented today. Because the underlying Google API is itself in **Beta** — and has made in-place breaking changes inside v1 — schemas here may still change before the first tagged release.

## Contents

- [Quickstart](#quickstart)
- [Authentication setup](#authentication-setup)
- [Destroy means archive](#destroy-means-archive)
- [Entity coverage](#entity-coverage)
- [Rate limiting](#rate-limiting)
- [Import](#import)
- [Testing](#testing)
- [Contributing](#contributing)
- [License](#license)

## Quickstart

Under five minutes from zero to a managed ad unit. This example creates a web ad unit directly under your network's root, serving a 300x250 rectangle and a 728x90 leaderboard.

```hcl
terraform {
  required_providers {
    admanager = {
      source  = "VictorCano/admanager"
      version = "~> 0.1"
    }
  }
}

provider "admanager" {
  network_code = "123456"           # or set ADMANAGER_NETWORK_CODE
  credentials  = "/path/to/sa.json" # or use Application Default Credentials
}

# The network data source exposes read-only network metadata, including the
# effective root ad unit that top-level inventory hangs from.
data "admanager_network" "current" {}

resource "admanager_ad_unit" "homepage" {
  # Top-level units hang off the network's root ad unit. You can also pass the
  # root resource name literally instead:
  #   parent_ad_unit = "networks/123456/adUnits/<root_id>"
  parent_ad_unit = data.admanager_network.current.effective_root_ad_unit

  display_name = "Homepage"
  description  = "Homepage inventory"

  # Optional, immutable ad serving code. If omitted, Ad Manager assigns one.
  ad_unit_code = "homepage"

  sizes = [
    {
      width            = 300
      height           = 250
      size_type        = "PIXEL"
      environment_type = "BROWSER"
    },
    {
      width            = 728
      height           = 90
      size_type        = "PIXEL"
      environment_type = "BROWSER"
    },
  ]
}
```

> **Not on the registry yet.** The `required_providers` block above is the shape you will use once `v0.1.0` is published to the Terraform Registry under `VictorCano/admanager` (**coming with `v0.1.0`**). Until then, build the provider from source and point Terraform/OpenTofu at your local binary with a [dev override](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers) (`~/.terraformrc` / `dev_overrides`).

### Provider configuration reference

```hcl
provider "admanager" {
  network_code = "123456"           # or env ADMANAGER_NETWORK_CODE
  credentials  = "/path/to/sa.json" # path or JSON content; or Application Default Credentials

  # Optional tuning — Ad Manager API quotas are low; defaults are conservative.
  requests_per_second = 2 # client-side rate limit (default 2)
  retry_max_attempts  = 5 # attempts per call: initial + retries (default 5)
}
```

- `network_code` — your Ad Manager network code. Falls back to the `ADMANAGER_NETWORK_CODE` environment variable.
- `credentials` — a path to, or the raw JSON content of, a Google service account key. When unset, [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials) are used (honoring `GOOGLE_APPLICATION_CREDENTIALS`).
- `requests_per_second`, `retry_max_attempts` — see [Rate limiting](#rate-limiting).

## Authentication setup

The provider authenticates as a Google **service account**. The steps below were validated live against Ad Manager on **2026-07-06**; the one non-obvious step (where the "Add a service account user" button hides in the GAM UI) trips up nearly everyone, so it is called out explicitly.

**1. Create a service account with Ad Manager API access.**

1. In a [Google Cloud](https://console.cloud.google.com) project, **enable the Ad Manager API** (`admanager.googleapis.com`) under *APIs & Services → Library*. This must be the same project the service account lives in.
2. Create a **service account** (*IAM & Admin → Service Accounts*). It needs no Google Cloud IAM roles — authorization happens inside Ad Manager, not GCP.
3. Create a **JSON key** for that service account and download it. This file is the `credentials` value.

**2. Turn on API access in Ad Manager, then add the service account as a user.**

In the Ad Manager UI, go to **Admin → Global settings → Network settings**:

1. Enable the **"API access"** toggle on that page and save.
2. **Only after API access is enabled** does the **"Add a service account user"** button appear — on that *same Network settings page*. It is **not** on the *Admin → Users* screen, which is where most people look first and give up. Calling this out saves real time.
3. Add a service account user using the **client email** from your JSON key (the `client_email` field). Assign it the **Administrator** role, or a trafficking role with enough permission to manage the inventory you intend to control.

That single service account works for everything this provider does, including custom targeting value writes over the [SOAP compatibility layer](#custom-targeting-values-use-a-soap-compatibility-layer) — no second credential is required.

**Using your own user credentials (ADC) instead of a service account?**

If you authenticate with user Application Default Credentials (`gcloud auth application-default login`) rather than a service account key, the REST API additionally requires a **quota project**, or calls fail with `SERVICE_DISABLED`. Set one explicitly:

```sh
gcloud auth application-default set-quota-project <your-gcp-project-with-admanager-enabled>
```

Service account keys carry their own project and do not need this.

## Destroy means archive

Google Ad Manager does not hard-delete most entities — it **archives** or **deactivates** them, and so does this provider. There is no code path here that hard-deletes anything, because the API offers none.

- `terraform destroy` on an **ad unit** or **placement** **archives** it in GAM (`adUnits:batchArchive` / `placements:batchArchive`).
- `terraform destroy` on a **custom targeting key** or **value** **deactivates** it — its status becomes `INACTIVE`. The API has no archive or hard delete for these.
- Every resource supports **`skip_archive_on_destroy = true`**, which removes the resource from Terraform state **without touching GAM at all**. This flag is provider-side only and is never sent to the API. It keeps the same name across all resources for consistency, even where the underlying operation is deactivation rather than archival.

## Entity coverage

`v0.1.0` targets the core inventory and targeting surface. The resources and read-only data sources below are all implemented today.

| Resource | GAM entity | Operations |
|---|---|---|
| `admanager_ad_unit` | AdUnit | create, read, update (field-mask patch), archive-on-destroy, import |
| `admanager_placement` | Placement | create, read, update (field-mask patch), archive-on-destroy, import |
| `admanager_custom_targeting_key` | CustomTargetingKey | create, read, update (field-mask patch), deactivate-on-destroy, import |
| `admanager_custom_targeting_value` | CustomTargetingValue | create, read, update, deactivate-on-destroy, import — writes via a [SOAP compatibility layer](#custom-targeting-values-use-a-soap-compatibility-layer) |

| Data source | Use |
|---|---|
| `admanager_network` | Network code, effective root ad unit, time zone, currency |
| `admanager_ad_unit` | Look up a single ad unit by numeric id, resource name, or ad serving code |
| `admanager_ad_units` | List / filter ad units in the inventory hierarchy |

### Not yet covered

The following are **out of scope for `v0.1.0`** and planned for future releases: **orders, line items, creatives, and reports**. Label management and label frequency caps on ad units are also deferred (the ad unit resource intentionally omits `applied_labels` and related attributes rather than shipping half-modeled fields with faked defaults). Contributions in these areas are welcome — see [Contributing](#contributing).

### Custom targeting values use a SOAP compatibility layer

Custom targeting **values** are **read-only in the Google REST API** (no create/update endpoints as of July 2026 — the December 2025 release added writes for ad units, placements, and custom targeting *keys*, but not values). To still offer them as a full resource, this provider reads values over REST and performs **writes through the legacy SOAP API** (`CustomTargetingService`) via a small internal compatibility layer. What this means for you:

- **The Terraform interface is identical to every other resource.** The SOAP layer is an implementation detail and will be removed transparently once Google ships value write endpoints in the REST API ([release notes](https://developers.google.com/ad-manager/api/beta/docs/release-notes)).
- **No extra credential setup.** The same service account and token work for both APIs (the provider requests the legacy SOAP scope alongside the REST scope defensively, but Google currently treats them as equivalent).
- **SOAP API versions sunset** on a rolling ~12-month schedule, so keeping the provider reasonably up to date matters more for this one resource than for the others. The pinned SOAP version and its version bumps are tracked in the [CHANGELOG](CHANGELOG.md).

## Rate limiting

Rate limiting is a **design requirement**, not an afterthought — Ad Manager quotas are low and largely undocumented. Every outbound API call goes through a client-side token bucket (default: **2 requests/second**, burst 1) with exponential backoff, jitter, and `Retry-After` support on quota errors. Retries are applied to `429` responses always, and to `5xx`/transport failures only for idempotent `GET`s (double-write protection).

`terraform plan` performs roughly one read per resource in state, so on large networks tune `requests_per_second` to your network's quota rather than disabling anything. `retry_max_attempts` caps total attempts per call (initial + retries).

## Import

Adoption is brownfield by design: **every resource implements `terraform import` from day one**, so you can bring existing GAM inventory under management without recreating it. Both a full resource name and a bare numeric ID work — a bare ID is expanded against the provider's configured network code:

```sh
terraform import admanager_ad_unit.homepage networks/123456/adUnits/789
terraform import admanager_ad_unit.homepage 789
```

## Testing

- **Unit tests need no credentials.** They run entirely against an in-process fake GAM API (`httptest`): `go test ./...` (add `-race` for concurrency-sensitive changes). This is what CI and the pre-push hook run.
- **Acceptance tests run only against a GAM test network.** They are gated behind `TF_ACC=1` and abort unless the network reports `testNetwork == true` — this guardrail exists precisely because destroy archives/deactivates rather than deletes, so a stray run against production would leave archived inventory behind. **Never** point `ADMANAGER_NETWORK_CODE` at a production network. Full instructions, including the required environment variables, are in [CONTRIBUTING.md](CONTRIBUTING.md#acceptance-tests).

## Contributing

Contributions are welcome — bug reports, new resources/data sources/attributes, and documentation fixes. See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow. Highlights:

- **Conventional Commits**, and every PR goes through an [adversarial review pass](.github/ADVERSARIAL_REVIEW.md) in addition to normal review — high-severity findings block merge.
- **Unit tests require no credentials**; acceptance tests run **only against GAM test networks**.
- `docs/` is **generated** by [`tfplugindocs`](https://github.com/hashicorp/terraform-plugin-docs) — edit `templates/` and `examples/`, never `docs/` directly.
- **CLA:** first-time contributors sign a one-time [Contributor License Agreement](CONTRIBUTING.md#pull-request-process) (the CLA Assistant bot prompts on your first PR); later PRs do not require it again.

## License

[MPL-2.0](LICENSE).
