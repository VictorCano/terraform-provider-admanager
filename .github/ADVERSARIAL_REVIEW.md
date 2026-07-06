# Adversarial Review Protocol

## Purpose

This project is solo-maintained. There is no second engineer who naturally
catches what the author missed, and no team review culture to fall back on.
To compensate, every pull request receives a dedicated **adversarial review**
pass, performed by a reviewer (human or LLM-based agent session) whose
explicit role is to find problems in the change, not to explain or defend it.
The adversarial reviewer has no authorship context and no incentive to see
the PR merged — their only job is to try to break it. The review result is
posted as a PR comment before merge is considered. High-severity findings
block merge; they are not deferred, waived, or merged around.

## Checklist

Every PR is checked against the following seven items. Absence of a finding
on an item should be stated explicitly in the review comment, not left
implicit.

1. **State corruption.** Can any error path leave Terraform state
   inconsistent with the actual state of Ad Manager? Look specifically for:
   an API call that partially succeeds (entity created in GAM) before the
   provider returns an error without recording the ID in state; an update
   that succeeds against the API but fails before `Set` populates the new
   state; missing state writes on any early return in Create/Update/Delete.

2. **Destructive operations.** Can any code path archive, deactivate, or
   modify an entity other than the one the resource is managing? Check
   cascading effects (e.g. an update to a parent that implicitly touches
   children), any bulk/batch API usage that could sweep in unrelated IDs,
   and any destroy behavior that would surprise a user reading the resource
   docs — including whether `skip_archive_on_destroy` is honored correctly
   and whether the default destroy behavior matches documented GAM semantics
   (archive/deactivate, not hard delete).

3. **Credential leakage.** Can service account JSON, private keys, access
   tokens, or refresh tokens reach logs, `tflog` output, error messages,
   Terraform diagnostics, HTTP request/response debug dumps, or test
   fixtures committed to the repo? Check error wrapping in the HTTP client
   layer for accidental inclusion of request headers or bodies.

4. **Rate limiting.** Does every outbound call to the GAM API go through the
   client's token bucket / rate limiter, or is there a code path (a new
   helper, a retry loop, a pagination loop) that bypasses it? Can a retry
   after a timeout or ambiguous response double-apply a write — i.e. is the
   operation actually idempotent, or could retrying a "did it apply?"
   failure create a duplicate entity or reapply a mutation twice?

5. **Swallowed errors.** Any `if err != nil { return err }` or generic
   diagnostic (`"an error occurred"`, `"unexpected error"`) where the
   underlying API response, entity ID, or operation name should be
   surfaced instead? Any ignored error return value (`_ = foo()`) that
   should be handled or at least logged with context?

6. **Honest drift.** Does `Read` faithfully reflect what the API actually
   returned, field for field, or is a computed/suppressed diff hiding real
   drift just to make plans look clean or acceptance tests pass? Check for
   `DiffSuppressFunc`-equivalent logic, hardcoded defaults masking server
   values, or fields silently omitted from the read-back.

7. **Schema fidelity.** Does every attribute in the resource/data-source
   schema match the current official GAM REST API reference for that
   field's name, type, nullability, and semantics? Verify against the live
   discovery document or current API reference — do not trust memory or
   assume a field is unchanged from a prior API version.

## Severity

- **High (blocks merge):** state corruption, credential leakage,
  destructive behavior beyond the resource at hand, silent drift
  suppression. These must be fixed in the PR before merge — no exceptions.
- **Medium:** should be fixed before merge, or the author opens a
  follow-up issue and links it in the PR before merging.
- **Low:** noted in the review comment; no action required to merge.

## Process

1. The reviewer works through the checklist in order and posts findings as
   a single PR comment, numbering each finding by the checklist item it
   corresponds to (e.g. `3. Credential leakage: ...`) and tagging its
   severity.
2. The author addresses each finding with a code change or an explicit
   rebuttal posted as a reply — silence is not an acceptable response to a
   finding.
3. Merge proceeds only once every high-severity finding is resolved or
   rebutted to the reviewer's satisfaction.
4. This checklist evolves with experience. If an incident or near-miss
   reveals a gap, add a new checklist item via its own PR rather than
   handling it as tribal knowledge.
