# terraform-provider-admanager

First Terraform/OpenTofu provider for Google Ad Manager (GAM), built on the
Ad Manager REST API v1 (`admanager.googleapis.com`). Solo-maintained; quality
comes from process — TDD, generated docs, and an adversarial review pass on
every PR (`.github/ADVERSARIAL_REVIEW.md`).

The full internal spec lives in `dev/spec.md` (gitignored, local only). Read
it at the start of any working session if present.

## Commands

- Build: `go build ./...`
- Unit tests (no credentials needed, httptest only): `go test -race ./...`
- Lint: `golangci-lint run` (binary in `$(go env GOPATH)/bin`)
- Docs: `go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest generate`
  — edit `templates/` and `examples/`, NEVER `docs/` (generated)
- Acceptance: `TF_ACC=1 ADMANAGER_NETWORK_CODE=... go test -v ./internal/provider/`
  — ONLY against a GAM test network; tests verify `Network.testNetwork == true` and abort otherwise
- Local run: see `.claude/skills/verify/SKILL.md` (OpenTofu dev overrides)
- Hooks: `pnpm install` once; pre-commit = gofmt + golangci-lint + trufflehog,
  pre-push = go test + trufflehog. Never bypass hooks.

## Architecture

- `internal/client` — hand-rolled REST client (no official Go client exists).
  ALL API calls go through `Client.do()`: token bucket (default 2 req/s,
  burst 1), retries (429 always; 5xx/transport only for GET — double-write
  protection), `Retry-After` support. Never bypass it.
- `internal/soap` (planned) — minimal SOAP shim, see decision below.
- `internal/provider` — plugin-framework provider, resources, data sources.

## Locked decisions (do not renegotiate without Victor)

1. Go + `terraform-plugin-framework` (never the legacy plugin-sdk/v2).
2. REST API is the primary API. Schema mirrors the live discovery doc
   (`https://admanager.googleapis.com/$discovery/rest?version=v1`) — never
   invent or trust remembered fields. The API is **Beta** and has made
   breaking changes inside v1: re-check the discovery doc and
   [release notes](https://developers.google.com/ad-manager/api/beta/docs/release-notes)
   at the start of each phase.
3. Rate limiting is a design requirement (GAM quotas are low and undocumented).
4. `terraform destroy` = archive (ad units, placements) or deactivate
   (custom targeting keys/values). Never hard-delete. Every resource has
   `skip_archive_on_destroy` to leave GAM untouched.
5. Import is first-class on every resource.
6. TDD always; adversarial review before merge; high-severity findings block.

## Decision (2026-07-06): SOAP shim for custom targeting values ("Option B")

Custom targeting **values** are read-only in the REST API (no create/update
as of July 2026 — writes exist for ad units, placements, and keys since the
2025-12-04 release, but not values). Victor decided to ship the
`admanager_custom_targeting_value` resource anyway, using a **minimal SOAP
compatibility shim**:

- **Reads stay on REST** (`networks.customTargetingValues.get/list`).
- **Writes go through SOAP** `CustomTargetingService`
  (`createCustomTargetingValues`, `updateCustomTargetingValues`,
  `performCustomTargetingValueAction` for deactivation), implemented as
  hand-built XML envelopes in `internal/soap` — no third-party SOAP library.
- OAuth scope for SOAP is `https://www.googleapis.com/auth/dfp` (the REST
  scope is `.../auth/admanager`); the token source must request both.
- **Version policy:** target a current SOAP version (v202511 or newer at
  implementation time). SOAP versions sunset ~12 months after release on a
  rolling quarterly schedule
  ([deprecation schedule](https://developers.google.com/ad-manager/api/deprecation)) —
  bump the shim's version constant at least twice a year; a stale version
  breaks value writes for every user.
- **Exit criterion:** the shim is deleted the moment the REST API ships value
  write endpoints (watch the release notes). The Terraform schema must not
  leak SOAP details, so the swap is invisible to users.
