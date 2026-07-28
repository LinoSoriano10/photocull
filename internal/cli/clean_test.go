package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// recordingMover implements trash.Mover without touching the real recycle bin,
// so these tests can assert exactly what clean would delete.
type recordingMover struct {
	moved []string
}

func (m *recordingMover) Move(paths ...string) error {
	m.moved = append(m.moved, paths...)
	return nil
}

// runCleanForTest drives runClean the way the cobra command would, but with an
// injected mover and a scripted stdin.
func runCleanForTest(t *testing.T, dir string, confirm, yes bool, stdin string) (*recordingMover, string, error) {
	t.Helper()

	mover := &recordingMover{}
	out := &strings.Builder{}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	opts := &cleanOptions{
		global:  &globalFlags{},
		match:   &matchFlags{strategy: "default", threshold: 8},
		confirm: confirm,
		yes:     yes,
		mover:   mover,
		in:      strings.NewReader(stdin),
		out:     out,
	}

	err := runClean(cmd, dir, opts)
	return mover, out.String(), err
}

// exactFixture is the committed exact-duplicate directory: original.jpg, an
// identical copy under backup/, and one unrelated photo.
const exactFixture = "../../testdata/exact"

func TestCleanDryRunMovesNothing(t *testing.T) {
	mover, out, err := runCleanForTest(t, exactFixture, false, false, "")
	if err != nil {
		t.Fatalf("clean dry run: %v", err)
	}
	if len(mover.moved) != 0 {
		t.Errorf("dry run moved files: %v", mover.moved)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("dry run output did not say so:\n%s", out)
	}
}

func TestCleanConfirmYesMovesDuplicate(t *testing.T) {
	mover, _, err := runCleanForTest(t, exactFixture, true, true, "")
	if err != nil {
		t.Fatalf("clean --confirm --yes: %v", err)
	}
	if len(mover.moved) != 1 {
		t.Fatalf("moved %v, want exactly the one duplicate", mover.moved)
	}
	// The keeper is original.jpg (shallowest path), so the copy under backup/
	// is what must be moved.
	if filepath.Base(filepath.Dir(mover.moved[0])) != "backup" {
		t.Errorf("moved %q, want the copy under backup/", mover.moved[0])
	}
}

func TestCleanPromptDeclined(t *testing.T) {
	mover, out, err := runCleanForTest(t, exactFixture, true, false, "n\n")
	if err != nil {
		t.Fatalf("clean with declined prompt: %v", err)
	}
	if len(mover.moved) != 0 {
		t.Errorf("declining the prompt still moved files: %v", mover.moved)
	}
	if !strings.Contains(strings.ToLower(out), "cancelled") {
		t.Errorf("output did not acknowledge cancellation:\n%s", out)
	}
}

func TestCleanPromptAccepted(t *testing.T) {
	mover, _, err := runCleanForTest(t, exactFixture, true, false, "y\n")
	if err != nil {
		t.Fatalf("clean with accepted prompt: %v", err)
	}
	if len(mover.moved) != 1 {
		t.Errorf("accepting the prompt moved %v, want one file", mover.moved)
	}
}

func TestCleanEmptyStdinDeclines(t *testing.T) {
	// A closed stdin (EOF, empty) must be treated as "no", never as consent.
	mover, _, err := runCleanForTest(t, exactFixture, true, false, "")
	if err != nil {
		t.Fatalf("clean with empty stdin: %v", err)
	}
	if len(mover.moved) != 0 {
		t.Errorf("empty stdin was treated as consent: %v", mover.moved)
	}
}

func TestCleanYesWithoutConfirmIsRejected(t *testing.T) {
	_, _, err := runCleanForTest(t, exactFixture, false, true, "")
	if err == nil {
		t.Error("--yes without --confirm should be rejected, not guessed at")
	}
}

func TestCleanReportsWhenNothingToDo(t *testing.T) {
	// A directory with no duplicates should clean happily and move nothing.
	mover, _, err := runCleanForTest(t, "../../testdata/corrupt", true, true, "")
	if err != nil {
		t.Fatalf("clean on a duplicate-free directory: %v", err)
	}
	if len(mover.moved) != 0 {
		t.Errorf("moved files from a directory with no duplicates: %v", mover.moved)
	}
}

// runCleanDocsForTest drives clean in documents mode over a directory.
func runCleanDocsForTest(t *testing.T, dir string, confirm, yes bool) (*recordingMover, string, error) {
	t.Helper()

	mover := &recordingMover{}
	out := &strings.Builder{}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	opts := &cleanOptions{
		global:  &globalFlags{kind: "docs"},
		match:   &matchFlags{similar: true, threshold: 6},
		confirm: confirm,
		yes:     yes,
		mover:   mover,
		in:      strings.NewReader(""),
		out:     out,
	}

	err := runClean(cmd, dir, opts)
	return mover, out.String(), err
}

// TestCleanNeverTouchesRelatedGroups is the security-critical test of this
// command, and it is written as a directory rather than a unit because the bug
// it guards against would only appear once everything was wired together.
//
// A related group is files matched on nothing but their names and sizes,
// because photocull could not read inside them. Acting on that is not a
// judgement call the tool is entitled to make. Without the skip in runClean,
// "clean --confirm --yes" would send them to the recycle bin.
func TestCleanNeverTouchesRelatedGroups(t *testing.T) {
	dir := t.TempDir()

	// Two files photocull cannot read inside, with names and sizes that line up
	// exactly — the strongest possible related match.
	body := strings.Repeat("\x00\x01\x02\x03", 4096)
	writeTestFile(t, filepath.Join(dir, "informe.bin"), body)
	writeTestFile(t, filepath.Join(dir, "informe (1).bin"), body+"tail")

	mover, out, err := runCleanDocsForTest(t, dir, true, true)
	if err != nil {
		t.Fatalf("clean --kind docs --confirm --yes: %v", err)
	}

	for _, moved := range mover.moved {
		t.Errorf("clean moved %q; files matched only by name and size must never be deleted automatically", moved)
	}
	if !strings.Contains(out, "related") {
		t.Errorf("clean did not tell the user the related groups were left alone:\n%s", out)
	}
}

// TestCleanStillDeletesExactDuplicatesInDocumentsMode is the other half: the
// skip must be narrow. A byte-identical pair is still safe to act on.
func TestCleanStillDeletesExactDuplicatesInDocumentsMode(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("the same document, twice over. ", 200)
	writeTestFile(t, filepath.Join(dir, "notes.txt"), body)
	writeTestFile(t, filepath.Join(dir, "backup", "notes.txt"), body)

	mover, _, err := runCleanDocsForTest(t, dir, true, true)
	if err != nil {
		t.Fatalf("clean --kind docs: %v", err)
	}
	if len(mover.moved) != 1 {
		t.Fatalf("moved %v, want exactly the one byte-identical copy", mover.moved)
	}
	if filepath.Base(filepath.Dir(mover.moved[0])) != "backup" {
		t.Errorf("moved %q, want the copy under backup/", mover.moved[0])
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
