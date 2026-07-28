package cli

import (
	"context"
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
