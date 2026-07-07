package ci

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// accGuardPath resolves acc_test_guard.sh relative to this test file so the
// test is independent of the working directory.
func accGuardPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "acc_test_guard.sh")
}

// runAccGuard runs the guard against dir and returns its exit code.
func runAccGuard(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("sh", accGuardPath(t), dir) //nolint:gosec // G204: dir is a test-controlled temp path.
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

// A well-formed acceptance TestCase: has CheckDestroy and a single step whose
// only assertions are checks. Written in the gofmt-shaped house style the guard
// depends on (one brace per line, config via a helper call).
const accGoodCase = `package p

func TestAccGood(t *testing.T) {
	resource.Test(t, resource.TestCase{
		CheckDestroy: testAccCheckGoodArchived(t),
		Steps: []resource.TestStep{
			{
				Config: testAccGoodConfig(),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("x", tfjsonpath.New("status"), knownvalue.StringExact("ACTIVE")),
				},
			},
		},
	})
}
`

func TestAccGuardPassesOnWellFormedCase(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "good_acc_test.go", accGoodCase)
	if code := runAccGuard(t, dir); code != 0 {
		t.Fatalf("exit = %d, want 0 for a well-formed TestCase", code)
	}
}

func TestAccGuardPassesOnEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if code := runAccGuard(t, dir); code != 0 {
		t.Fatalf("exit = %d, want 0 for a dir with no acc test files", code)
	}
}

// A data-source-only TestCase creates nothing to destroy; the opt-out marker
// tells the guard so, and the case must pass despite having no CheckDestroy.
func TestAccGuardPassesOnAnnotatedDataSourceCase(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "ds_acc_test.go", `package p

func TestAccDS(t *testing.T) {
	// acc-test-guard:no-checkdestroy — reads a data source, creates nothing.
	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: testAccDSConfig(),
			},
		},
	})
}
`)
	if code := runAccGuard(t, dir); code != 0 {
		t.Fatalf("exit = %d, want 0 for an annotated data-source-only case", code)
	}
}

// A resource TestCase with no CheckDestroy and no opt-out marker must fail
// (rule a). Without this the archive-semantics guarantee is silently dropped.
func TestAccGuardFailsOnMissingCheckDestroy(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "bad_acc_test.go", `package p

func TestAccBad(t *testing.T) {
	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: testAccBadConfig(),
				Check:  resource.TestCheckResourceAttr("x", "y", "z"),
			},
		},
	})
}
`)
	if code := runAccGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (TestCase without CheckDestroy)", code)
	}
}

// A step that sets BOTH ExpectError and Check must fail (rule b): the check is
// silently ignored by the harness.
func TestAccGuardFailsOnExpectErrorWithCheck(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "bad_acc_test.go", `package p

func TestAccBad(t *testing.T) {
	resource.Test(t, resource.TestCase{
		CheckDestroy: testAccCheckBadInactive(t),
		Steps: []resource.TestStep{
			{
				Config:      testAccBadConfig(),
				ExpectError: regexp.MustCompile("boom"),
				Check:       resource.TestCheckResourceAttr("x", "y", "z"),
			},
		},
	})
}
`)
	if code := runAccGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (ExpectError + Check in one step)", code)
	}
}

// Same footgun via ConfigStateChecks rather than the legacy Check field.
func TestAccGuardFailsOnExpectErrorWithConfigStateChecks(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "bad_acc_test.go", `package p

func TestAccBad(t *testing.T) {
	resource.Test(t, resource.TestCase{
		CheckDestroy: testAccCheckBadInactive(t),
		Steps: []resource.TestStep{
			{
				Config:      testAccBadConfig(),
				ExpectError: regexp.MustCompile("boom"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("x", tfjsonpath.New("y"), knownvalue.NotNull()),
				},
			},
		},
	})
}
`)
	if code := runAccGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (ExpectError + ConfigStateChecks in one step)", code)
	}
}

// An ExpectError step and a checks step that are SEPARATE steps are fine — the
// footgun is only when both live in the same step. This isolates rule (b) from
// a naive per-TestCase (rather than per-step) implementation.
func TestAccGuardPassesOnExpectErrorAndChecksInSeparateSteps(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "ok_acc_test.go", `package p

func TestAccOK(t *testing.T) {
	resource.Test(t, resource.TestCase{
		CheckDestroy: testAccCheckOKInactive(t),
		Steps: []resource.TestStep{
			{
				Config: testAccOKConfig(),
				Check:  resource.TestCheckResourceAttr("x", "y", "z"),
			},
			{
				Config:      testAccOKDuplicateConfig(),
				ExpectError: regexp.MustCompile("boom"),
			},
		},
	})
}
`)
	if code := runAccGuard(t, dir); code != 0 {
		t.Fatalf("exit = %d, want 0 (ExpectError and checks in separate steps)", code)
	}
}

// A comment merely mentioning "CheckDestroy:" must NOT satisfy rule (a): the
// guard has to match a real struct-field assignment, not the field name in
// prose. Without anchoring, a stray comment silently exempts a resource case.
func TestAccGuardFailsWhenOnlyACommentMentionsCheckDestroy(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "bad_acc_test.go", `package p

func TestAccBad(t *testing.T) {
	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				// NOTE: CheckDestroy: mentioned here but never actually set.
				Config: testAccBadConfig(),
				Check:  resource.TestCheckResourceAttr("x", "y", "z"),
			},
		},
	})
}
`)
	if code := runAccGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (a comment mentioning CheckDestroy must not exempt a resource case)", code)
	}
}

// The opt-out marker must not leak from a far-away prose mention into an
// unrelated resource case. A marker in a comment separated from the TestCase by
// intervening code (here a helper func) is not an exemption for that case.
func TestAccGuardFailsWhenMarkerIsFarFromTheCase(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "bad_acc_test.go", `package p

// Historical note: an earlier draft used acc-test-guard:no-checkdestroy here.
func helper() int { return 0 }

func TestAccBad(t *testing.T) {
	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: testAccBadConfig(),
				Check:  resource.TestCheckResourceAttr("x", "y", "z"),
			},
		},
	})
}
`)
	if code := runAccGuard(t, dir); code != 1 {
		t.Fatalf("exit = %d, want 1 (a far-away marker must not exempt an unrelated case)", code)
	}
}

// A comment containing "Check:" inside an ExpectError-only step must NOT trip
// rule (b): the check-detection has to match a real field, not the token in a
// comment, otherwise a legitimate ExpectError step is falsely blocked.
func TestAccGuardPassesWhenCommentMentionsCheckOnExpectErrorStep(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "ok_acc_test.go", `package p

func TestAccOK(t *testing.T) {
	resource.Test(t, resource.TestCase{
		CheckDestroy: testAccCheckOKInactive(t),
		Steps: []resource.TestStep{
			{
				// Check: nothing else needed on this step, the error is enough.
				Config:      testAccOKConfig(),
				ExpectError: regexp.MustCompile("boom"),
			},
		},
	})
}
`)
	if code := runAccGuard(t, dir); code != 0 {
		t.Fatalf("exit = %d, want 0 (a comment mentioning Check: must not trip rule b)", code)
	}
}

// Live guard against the real acceptance suite: it must pass today.
func TestAccGuardPassesOnRealProvider(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	if code := runAccGuard(t, filepath.Join(repoRoot, "internal", "provider")); code != 0 {
		t.Fatalf("exit = %d, want 0 for the real internal/provider acceptance suite", code)
	}
}
