#!/bin/sh
# acc_test_guard.sh — CI guard for acceptance-test hygiene.
#
# Two footguns this catches in internal/provider/*_acc_test.go:
#
#   (a) A resource.TestCase without a CheckDestroy. On this provider destroy
#       means archive/deactivate (never hard-delete, locked decision 4); the
#       CheckDestroy helper is the ONLY thing that asserts that archive
#       semantics actually happened on the live network. A TestCase that
#       creates managed resources but omits CheckDestroy silently drops that
#       guarantee. A data-source-only TestCase that creates nothing legitimately
#       needs none: annotate it with `// acc-test-guard:no-checkdestroy`.
#
#   (b) A TestStep that sets BOTH ExpectError and a check (Check or
#       ConfigStateChecks). terraform-plugin-testing silently ignores the checks
#       on an ExpectError step, so this reads as a passing assertion while
#       asserting nothing — a classic false-confidence footgun.
#
# Grep/awk-based and self-contained. Usage: acc_test_guard.sh [dir] (default
# internal/provider). It relies on gofmt-shaped TestCase/TestStep literals
# (one brace per line, config supplied via helper-function calls rather than
# inline heredocs) — the house style for this repo's acceptance files. Brace
# tracking begins only inside a `resource.TestCase{` literal, so the HCL braces
# in the config helper functions (which live outside any TestCase literal) never
# perturb it.
set -eu

dir="${1:-internal/provider}"

status=0
for f in "$dir"/*_acc_test.go; do
	[ -e "$f" ] || continue

	# awk emits one "<line>\t<kind>" record per violation; kind is either
	# "no-checkdestroy" (rule a) or "checks-with-expecterror" (rule b).
	violations="$(awk '
		function reset_case() { in_case=0; depth=0; has_cd=0; opt_out=0; case_line=0 }
		function reset_step() { in_step=0; step_has_ee=0; step_has_check=0 }
		BEGIN { reset_case(); reset_step() }
		{
			# Field detection is anchored to the start of the (whitespace-
			# stripped) line so it matches a real struct-field assignment and
			# never a mention of the field name inside a // comment. The opt-out
			# marker is a comment annotation by design, so it is honored ONLY on
			# a comment line — and, before a case opens, only while it sits in a
			# contiguous run of comment/blank lines directly above the TestCase
			# (see pending_opt handling below).
			trimmed = $0
			sub(/^[[:space:]]+/, "", trimmed)
			is_comment = (substr(trimmed, 1, 2) == "//")
			is_blank = (trimmed == "")
			has_marker = (is_comment && $0 ~ /acc-test-guard:no-checkdestroy/)

			# Count braces on this line (structural only: inside a TestCase
			# literal there are no inline HCL heredocs).
			opens = gsub(/[{]/, "{"); closes = gsub(/[}]/, "}")

			if (!in_case) {
				if ($0 ~ /resource\.TestCase\{/) {
					in_case = 1; has_cd = 0; opt_out = pending_opt; case_line = NR
					pending_opt = 0
					depth = opens - closes
					reset_step()
					next
				}
				# A marker comment stays pending across the contiguous run of
				# comment/blank lines directly above the TestCase it exempts; any
				# other code line breaks that run and clears it, so a far-away
				# prose mention cannot leak into an unrelated case.
				if (has_marker) pending_opt = 1
				else if (!is_comment && !is_blank) pending_opt = 0
				next
			}

			pre = depth
			depth += opens - closes

			# TestCase-level flags.
			if (trimmed ~ /^CheckDestroy:/) has_cd = 1
			if (has_marker) opt_out = 1

			# Step boundaries: a step is a brace block one level below the
			# Steps slice, i.e. it opens crossing depth 2 -> 3 and closes 3 -> 2.
			if (pre == 2 && depth >= 3 && !in_step) { in_step = 1; step_has_ee = 0; step_has_check = 0 }

			if (in_step && depth >= 3) {
				if (trimmed ~ /^ExpectError:/) step_has_ee = 1
				if (trimmed ~ /^Check:/ || trimmed ~ /^ConfigStateChecks:/) step_has_check = 1
			}

			if (in_step && depth <= 2) {
				if (step_has_ee && step_has_check)
					printf "%d\tchecks-with-expecterror\n", NR
				reset_step()
			}

			# TestCase closed.
			if (depth <= 0) {
				if (!has_cd && !opt_out)
					printf "%d\tno-checkdestroy\n", case_line
				reset_case(); reset_step()
			}
		}
	' "$f")"

	[ -z "$violations" ] && continue

	printf '%s\n' "$violations" | while IFS='	' read -r ln kind; do
		[ -z "$ln" ] && continue
		case "$kind" in
		no-checkdestroy)
			echo "::error file=${f},line=${ln}::resource.TestCase has no CheckDestroy. Destroy on this provider must archive/deactivate (locked decision 4); add a CheckDestroy that asserts it, or annotate a resource-free data-source case with '// acc-test-guard:no-checkdestroy'." >&2
			;;
		checks-with-expecterror)
			echo "::error file=${f},line=${ln}::TestStep sets both ExpectError and Check/ConfigStateChecks. terraform-plugin-testing silently ignores the checks on an ExpectError step; split them into separate steps." >&2
			;;
		esac
	done
	status=1
done

if [ "$status" -eq 0 ]; then
	echo "acc-test guard: every resource.TestCase has a CheckDestroy and no TestStep mixes ExpectError with checks; OK"
fi
exit "$status"
