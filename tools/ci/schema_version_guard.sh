#!/bin/sh
# schema_version_guard.sh — CI guard for silent state-migration breaks.
#
# terraform-plugin-framework resources declare their schema version via the
# schema.Schema `Version` field and migrate old state with the optional
# ResourceWithUpgradeState interface (a `UpgradeState(...)` method). Bumping the
# version WITHOUT shipping upgraders — and a test for them — silently corrupts
# every existing user's state on the next apply.
#
# This guard fails when any resource in the target directory sets a schema
# version above 0 unless BOTH are present in that package:
#   1. a StateUpgraders implementation (a `UpgradeState(` method), and
#   2. a matching *upgrade*_test.go.
#
# It is intentionally grep-based and self-contained. Today no resource sets a
# version, so it passes. Usage: schema_version_guard.sh [dir] (default
# internal/provider).
set -eu

dir="${1:-internal/provider}"

# Non-test Go files that set a schema Version (or SchemaVersion) above zero.
bump_files=""
for f in "$dir"/*.go; do
	[ -e "$f" ] || continue
	case "$f" in
	*_test.go) continue ;;
	esac
	if grep -Eq '(SchemaVersion|Version):[[:space:]]*[1-9]' "$f"; then
		bump_files="${bump_files}${f}
"
	fi
done

if [ -z "$bump_files" ]; then
	echo "schema-version guard: no resource sets a schema version above 0; OK"
	exit 0
fi

printf 'schema-version guard: schema version bump detected in:\n' >&2
printf '%s' "$bump_files" | sed 's/^/  /' >&2

if ! grep -rlE 'func .*UpgradeState\(' "$dir"/*.go >/dev/null 2>&1; then
	echo "::error::A schema version above 0 requires a StateUpgraders implementation (a 'func ... UpgradeState(...)' method / ResourceWithUpgradeState) in ${dir}, but none was found. Add upgraders before bumping the version." >&2
	exit 1
fi

# shellcheck disable=SC2144 # intentional glob existence check across the dir
upgrade_test=""
for t in "$dir"/*upgrade*_test.go; do
	[ -e "$t" ] || continue
	upgrade_test="$t"
	break
done
if [ -z "$upgrade_test" ]; then
	echo "::error::A schema version above 0 requires a matching *upgrade*_test.go in ${dir}, but none was found. Add an upgrade test before bumping the version." >&2
	exit 1
fi

echo "schema-version guard: schema version bump is accompanied by StateUpgraders and an upgrade test; OK"
