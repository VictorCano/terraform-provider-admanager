package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// scriptPath resolves schema_version_guard.sh relative to this test file so the
// test is independent of the working directory.
func scriptPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "schema_version_guard.sh")
}

// runGuard runs the guard against dir and returns its exit code.
func runGuard(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("sh", scriptPath(t), dir) //nolint:gosec // G204: dir is a test-controlled temp path.
	out, err := cmd.CombinedOutput()
	t.Logf("guard output:\n%s", out)
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("running guard: %v", err)
	return -1
}

func writeGo(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestGuardPassesWithNoVersionBump(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "resource.go", "package p\n\nvar schema = struct{ Version int }{Version: 0}\n")
	if code := runGuard(t, dir); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestGuardPassesWithEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if code := runGuard(t, dir); code != 0 {
		t.Fatalf("exit = %d, want 0 for a dir with no Go files", code)
	}
}

func TestGuardFailsOnBumpWithoutUpgraders(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "resource.go", "package p\n\n// schema.Schema literal\nvar _ = map[string]int{}\nconst _ = 0\n// Version: 1\nvar s = struct{ Version int }{Version: 1}\n")
	if code := runGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (bump without upgraders)", code)
	}
}

func TestGuardFailsOnBumpWithUpgradersButNoTest(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "resource.go", "package p\n\nvar s = struct{ Version int }{Version: 2}\n")
	writeGo(t, dir, "upgrade.go", "package p\n\nfunc (r res) UpgradeState() {}\n")
	if code := runGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (bump + upgraders but no upgrade test)", code)
	}
}

// TestGuardFailsOnBumpWithoutUpgradersEvenWithUpgradeTest isolates the
// StateUpgraders gate (schema_version_guard.sh:43-46) from the upgrade-test
// gate. A matching *upgrade*_test.go IS present, so the second gate is
// satisfied; only the missing UpgradeState method can produce the failure.
// Without this fixture a mutant deleting the StateUpgraders check ships green.
func TestGuardFailsOnBumpWithoutUpgradersEvenWithUpgradeTest(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "resource.go", "package p\n\nvar s = struct{ Version int }{Version: 1}\n")
	writeGo(t, dir, "resource_upgrade_test.go", "package p\n")
	if code := runGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (bump + upgrade test but no upgraders)", code)
	}
}

// TestGuardFailsOnBumpWithNonUpgradeTest isolates the naming specificity of the
// upgrade-test gate (schema_version_guard.sh:50). Upgraders ARE present and a
// _test.go exists, but it is not *upgrade*-named, so the guard must still fail.
// Without this fixture a mutant broadening the glob to *_test.go ships green.
func TestGuardFailsOnBumpWithNonUpgradeTest(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "resource.go", "package p\n\nvar s = struct{ Version int }{Version: 2}\n")
	writeGo(t, dir, "upgrade.go", "package p\n\nfunc (r res) UpgradeState() {}\n")
	writeGo(t, dir, "resource_test.go", "package p\n")
	if code := runGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (bump + upgraders but only a non-upgrade test)", code)
	}
}

func TestGuardPassesOnBumpWithUpgradersAndTest(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "resource.go", "package p\n\nvar s = struct{ Version int }{Version: 2}\n")
	writeGo(t, dir, "upgrade.go", "package p\n\nfunc (r res) UpgradeState() {}\n")
	writeGo(t, dir, "resource_upgrade_test.go", "package p\n")
	if code := runGuard(t, dir); code != 0 {
		t.Fatalf("exit = %d, want 0 (bump + upgraders + upgrade test)", code)
	}
}

// TestGuardPassesOnRealProvider is a live guard against the actual provider
// package: it must pass today because no resource declares a schema version.
func TestGuardPassesOnRealProvider(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	if code := runGuard(t, filepath.Join(repoRoot, "internal", "provider")); code != 0 {
		t.Fatalf("exit = %d, want 0 for the real internal/provider package", code)
	}
}
