# Contributing to terraform-provider-admanager

Thanks for your interest in contributing. This is the first Terraform/OpenTofu
provider for Google Ad Manager (GAM), maintained by a single person, so clear
bug reports and focused pull requests make an outsized difference.

## Scope

Contributions are welcome in three areas:

- **Bug reports** — incorrect behavior against the GAM API, provider crashes,
  incorrect Terraform schema/state handling.
- **Features** — new resources, data sources, or attributes mapping to GAM
  API capabilities. For anything nontrivial, open an issue to discuss the
  design before writing code.
- **Documentation** — corrections or improvements to the generated docs,
  guides, or examples.

Please search existing issues before opening a new one.

## Development Setup

1. Clone the repository:

   ```sh
   git clone https://github.com/VictorCano/terraform-provider-admanager.git
   cd terraform-provider-admanager
   ```

2. Install Go 1.25 or later.

3. Run `pnpm install` once to activate the Git hooks (Husky). This is only
   needed for contributors — end users of the provider never need Node or
   pnpm.

4. Install the tools the hooks rely on locally, since Husky invokes them
   directly and does not install them for you:
   - [golangci-lint](https://golangci-lint.run/welcome/install/)
   - [trufflehog](https://github.com/trufflesecurity/trufflehog#installation)

Hooks are not optional: pre-commit runs a `gofmt` check, `golangci-lint`, and
a `trufflehog` secret scan; pre-push runs `go test` and `trufflehog` again.
Do not bypass them with `--no-verify` or similar flags — if a hook fails, fix
the underlying issue.

## Building and Testing

```sh
go build ./...
go test ./...
```

Unit tests use `httptest` against a fake GAM API and require no credentials.
Running with `-race` is welcome, especially for changes touching concurrency
or shared state: `go test -race ./...`.

## Acceptance Tests

Acceptance tests exercise the provider against a real GAM API and are gated
behind `TF_ACC=1`:

```sh
TF_ACC=1 \
ADMANAGER_NETWORK_CODE=<test-network-code> \
GOOGLE_APPLICATION_CREDENTIALS=<path-to-service-account.json> \
go test ./... -v
```

**Hard rule: acceptance tests must only ever run against a Google Ad Manager
TEST network.** Never point `ADMANAGER_NETWORK_CODE` at a production network.
GAM does not hard-delete most entities — `terraform destroy` archives or
deactivates them — so running acceptance tests against production would leave
archived resources behind in a live network. PRs that appear to have been
tested against, or that risk being run against, a production network will be
rejected.

If you don't have access to a GAM test network, note that in the PR
description; the maintainer can run acceptance tests before merge.

## Documentation

The `docs/` directory is **generated** by
[`tfplugindocs`](https://github.com/hashicorp/terraform-plugin-docs) and must
never be edited by hand — changes will be overwritten. To change
documentation:

1. Edit the relevant files under `templates/` and `examples/`.
2. Regenerate `docs/` with `tfplugindocs generate`.
3. Commit both the template/example changes and the regenerated `docs/`
   output in the same PR.

## Commit Conventions

This project requires [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add resource for ad manager placement
fix: correct pagination handling in line item data source
docs: clarify skip_archive_on_destroy behavior
refactor: extract retry logic from client package
```

Use a scope when it helps (`feat(client): ...`, `fix(provider): ...`). Keep
the subject line imperative and under about 72 characters.

## Pull Request Process

1. Fork the repository and create a branch off `main`.
2. Make your changes, including tests and documentation updates as
   applicable.
3. Open a pull request against `main`. CI must be green before merge: build,
   `go test`, `gofmt`, `golangci-lint`, and `trufflehog` all run in CI in
   addition to the local hooks.
4. If this is your first contribution, the CLA Assistant bot will comment on
   your PR asking you to sign the Contributor License Agreement — a one-time
   signature; later PRs will not require it again. PRs cannot be merged until
   the CLA is signed.
5. Every PR goes through an adversarial review pass against the checklist in
   [`.github/ADVERSARIAL_REVIEW.md`](.github/ADVERSARIAL_REVIEW.md), in
   addition to normal code review. This is a deliberately skeptical pass for
   edge cases, state-handling bugs, and GAM API misuse, so expect pointed
   questions even on changes that look straightforward. High-severity
   findings block merge until resolved.
6. Merges to `main` happen only via pull request, using squash merge, to keep
   a linear history.

## Where to Ask Questions

Use [GitHub Issues](https://github.com/VictorCano/terraform-provider-admanager/issues)
for bug reports and concrete feature proposals, and
[GitHub Discussions](https://github.com/VictorCano/terraform-provider-admanager/discussions)
(if enabled) for open-ended questions, usage help, or design discussion before
you invest time in a PR.
