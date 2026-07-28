package trash

import (
	"errors"
	"testing"
)

// FakeMover records what it was asked to move and can be told to fail specific
// paths. Every test in photocull that involves deletion uses one of these, so
// the real recycle bin is never touched by the test suite.
type FakeMover struct {
	Moved []string
	Fail  map[string]error
}

func (f *FakeMover) Move(paths ...string) error {
	var err error
	for _, p := range paths {
		if f.Fail != nil {
			if e, bad := f.Fail[p]; bad {
				err = errors.Join(err, e)
				continue
			}
		}
		f.Moved = append(f.Moved, p)
	}
	return err
}

// TestFakeMoverRecordsPaths is a sanity check on the test double itself: the
// rest of the suite trusts it to record exactly what a real Mover would move.
func TestFakeMoverRecordsPaths(t *testing.T) {
	var m FakeMover
	if err := m.Move("a.jpg", "b.jpg"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if len(m.Moved) != 2 {
		t.Errorf("recorded %d paths, want 2", len(m.Moved))
	}
}

func TestFakeMoverReportsFailuresButKeepsGoing(t *testing.T) {
	m := FakeMover{Fail: map[string]error{"bad.jpg": errors.New("locked")}}

	err := m.Move("good1.jpg", "bad.jpg", "good2.jpg")
	if err == nil {
		t.Fatal("expected an error for the failing path")
	}
	if len(m.Moved) != 2 {
		t.Errorf("a failure stopped the batch: moved %v, want the two good files", m.Moved)
	}
}

// SystemBin.Move is deliberately not exercised against the real recycle bin
// here; doing so would move real files during `go test`. This test only pins
// the contract that an empty call is a no-op success.
func TestSystemBinEmptyMoveIsNoOp(t *testing.T) {
	if err := (SystemBin{}).Move(); err != nil {
		t.Errorf("moving nothing returned an error: %v", err)
	}
}
