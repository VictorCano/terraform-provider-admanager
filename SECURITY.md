# Security Policy

## Supported Versions

This project is currently in initial development (`v0.x`). Only the latest
published release receives security fixes. There is no backport support for
older `v0.x` releases.

| Version        | Supported     |
| -------------- | ------------- |
| Latest release | Supported     |
| Older releases | Not supported |

Once the provider reaches `v1.0.0`, this policy will be revisited and a
supported-versions table with longer-lived branches will be published.

## Reporting a Vulnerability

Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.

**Primary channel: GitHub private vulnerability reporting.**
Go to the repository's [Security tab](https://github.com/VictorCano/terraform-provider-admanager/security)
and select **Report a vulnerability**. This creates a private advisory visible
only to the maintainer and lets us discuss and coordinate a fix before any
public disclosure.

**Fallback channel: email.**
If you are unable to use GitHub's private reporting for any reason, email
**victortroz@gmail.com** with as much detail as you can provide, including:

- A description of the vulnerability and its potential impact.
- Steps to reproduce, including provider version, Terraform/OpenTofu version,
  and relevant configuration (with any real credentials or account
  identifiers redacted).
- Any proof-of-concept code, logs, or state file excerpts that demonstrate
  the issue (again, with real secrets removed).

### What to expect

This project is maintained by a single volunteer maintainer. Response times
are best-effort:

- **Acknowledgement** of a report within **7 days**.
- A best-effort assessment and, where applicable, a remediation timeline
  communicated back to the reporter once triage is complete.
- Credit to the reporter in the release notes or security advisory, unless
  you request otherwise.

There is no bug bounty program associated with this project.

## Scope

This policy covers the `terraform-provider-admanager` codebase and its
release artifacts. Areas of particular concern include:

- **Credential handling.** The provider authenticates to Google Ad Manager
  using Google service account credentials. The provider must never write
  credential material (service account keys, access tokens, refresh tokens)
  to Terraform state, plan output, logs, or diagnostics.
- **Logging and diagnostics.** Sensitive values (credentials, tokens,
  authentication headers) must never appear in provider logs, `TF_LOG`
  output, or error/diagnostic messages surfaced to the user.
- **State exposure.** Any defect that causes secret material to be persisted
  in Terraform state in plaintext beyond what is inherent to Terraform's own
  state model should be reported.

Any report demonstrating that service account credentials or other secret
material leaks into state, logs, or diagnostics will be treated as a
**high-severity** issue and prioritized accordingly.

Out of scope: vulnerabilities in Terraform/OpenTofu core, in the Google Ad
Manager API or Google Cloud infrastructure itself, or in third-party
dependencies that are not exercised by this provider's code paths. If you
find an issue in a dependency, please also report it upstream.
