package storage

import "testing"

// TestDHTCacheRAMFormula asserts the formula's true computed output for the
// three declared-storage sizes build.md's own sanity-check prose names.
// See RequiredDHTCacheRAMMB's doc comment for the flagged ~2% discrepancy
// between the formula's exact output at 200GB (157MB) and the prose's
// stated 160MB — this test asserts the mathematically correct value.
func TestDHTCacheRAMFormula(t *testing.T) {
	cases := []struct {
		declaredGB uint64
		wantMB     uint64
	}{
		{50, 40},
		{200, 157},
		{500, 391}, // build.md's prose: "~400 MB" — already hedged as approximate
	}
	for _, c := range cases {
		got := RequiredDHTCacheRAMMB(c.declaredGB)
		if got != c.wantMB {
			t.Errorf("RequiredDHTCacheRAMMB(%d) = %d MB, want %d MB", c.declaredGB, got, c.wantMB)
		}
	}
}

func TestChunksPerGBIsCorrectedConstant(t *testing.T) {
	// A1: the corrected constant is 4096, not the erroneous ×400-derived
	// value from the pre-fix formula.
	if ChunksPerGB != 4096 {
		t.Errorf("ChunksPerGB = %d, want 4096", ChunksPerGB)
	}
}
