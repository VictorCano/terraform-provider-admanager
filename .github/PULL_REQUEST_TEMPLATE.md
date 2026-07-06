## Description

<!-- Summarize the change and the motivation behind it. -->

## Linked issue

<!-- e.g. Closes #123, Fixes #123, Relates to #123 -->

## Type of change

<!-- Check the one that matches your commit type (Conventional Commits). -->

- [ ] `feat`: new feature
- [ ] `fix`: bug fix
- [ ] `docs`: documentation only
- [ ] `chore`: tooling, build, or maintenance
- [ ] `test`: tests only
- [ ] `refactor`: code change with no behavior change

## Checklist

- [ ] PR title follows Conventional Commits style (e.g. `feat: add cdn_configuration resource`)
- [ ] Unit tests added or updated and passing locally (`go test ./...`)
- [ ] Acceptance tests run when this PR touches resource CRUD, and only against a GAM **TEST** network (never production)
- [ ] Docs regenerated with `tfplugindocs` when schemas changed (edited `templates/`/`examples/`, not `docs/` directly)
- [ ] No secrets, credentials, or network-specific identifiers anywhere in the diff
- [ ] `CHANGELOG.md` updated under `[Unreleased]` when the change is user-facing
- [ ] I have signed, or will sign, the CLA (required for external contributions)
