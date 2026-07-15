// Package erasure is declared in doc.go.
// Unit tests for rs_internal.go's GF(2^8) primitives and matrix operations.
// Previously untested in isolation — every existing test in engine_test.go
// operates at the Engine.EncodeSegment/DecodeSegment black-box level. Since
// this file replaced an external, pre-tested dependency
// (github.com/klauspost/reedsolomon) with ~250 lines of hand-written linear
// algebra (see doc.go's design notes), it deserves the focused unit tests
// that dependency never needed anyone to write.
//
// Every assertion here was independently verified against a from-scratch
// Python re-implementation of the same GF(2^8) construction (irreducible
// polynomial 0x11d) before being written — see the M3 review corrections
// note for the exact checks run.
//
// [REF: IC §5.2, ADR-003, M3 review §4]

package erasure

import "testing"

// TestGFMulInverseAxiom verifies gfMul(gfInv(x), x) == 1 for every nonzero
// x in GF(2^8) — the defining property of a multiplicative inverse.
func TestGFMulInverseAxiom(t *testing.T) {
	for x := 1; x < 256; x++ {
		if got := gfMul(gfInv(byte(x)), byte(x)); got != 1 {
			t.Errorf("gfMul(gfInv(%d), %d) = %d, want 1", x, x, got)
		}
	}
}

// TestGFMulCommutative verifies gfMul(a,b) == gfMul(b,a) exhaustively over
// all 256×256 byte pairs — cheap enough to run exhaustively rather than sample.
func TestGFMulCommutative(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			ab, ba := gfMul(byte(a), byte(b)), gfMul(byte(b), byte(a))
			if ab != ba {
				t.Fatalf("gfMul(%d,%d)=%d != gfMul(%d,%d)=%d", a, b, ab, b, a, ba)
			}
		}
	}
}

// TestGFMulZeroAbsorption verifies gfMul(x,0) == gfMul(0,x) == 0 for all x.
func TestGFMulZeroAbsorption(t *testing.T) {
	for x := 0; x < 256; x++ {
		if got := gfMul(byte(x), 0); got != 0 {
			t.Errorf("gfMul(%d, 0) = %d, want 0", x, got)
		}
		if got := gfMul(0, byte(x)); got != 0 {
			t.Errorf("gfMul(0, %d) = %d, want 0", x, got)
		}
	}
}

// TestGFMulDistributive spot-checks a*(b^c) == a*b^a*c over a deterministic
// pseudo-random sample of 2000 triples (full 256^3 exhaustion is unnecessary
// for a property this well-established for GF(2^n)).
func TestGFMulDistributive(t *testing.T) {
	seed := uint32(12345)
	next := func() byte {
		seed = seed*1664525 + 1013904223 // deterministic LCG
		return byte(seed >> 24)
	}
	for i := 0; i < 2000; i++ {
		a, b, c := next(), next(), next()
		lhs := gfMul(a, b^c)
		rhs := gfMul(a, b) ^ gfMul(a, c)
		if lhs != rhs {
			t.Fatalf("distributivity failed for a=%d b=%d c=%d: a*(b^c)=%d, a*b^a*c=%d",
				a, b, c, lhs, rhs)
		}
	}
}

// TestGFPowZeroExponent verifies gfPow(x, 0) == 1 for all x, including
// x == 0 — the code special-cases power==0 before checking x==0, so 0^0 is
// defined as 1 here by deliberate construction, not by accident.
func TestGFPowZeroExponent(t *testing.T) {
	for x := 0; x < 256; x++ {
		if got := gfPow(byte(x), 0); got != 1 {
			t.Errorf("gfPow(%d, 0) = %d, want 1", x, got)
		}
	}
}

// TestGFInvPanicsOnZero verifies gfInv(0) panics — zero has no
// multiplicative inverse in any field.
func TestGFInvPanicsOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("gfInv(0): expected panic, got none")
		}
	}()
	gfInv(0)
}

// TestPrimitiveElementGeneratesFullGroup verifies primitiveElement (2)
// generates all 255 nonzero elements of GF(2^8) — the property
// buildGeneratorMatrix's Vandermonde construction depends on to guarantee
// any k-subset of encoding-matrix rows is invertible, up to n=255 total
// shards (comfortably beyond production's n=56).
func TestPrimitiveElementGeneratesFullGroup(t *testing.T) {
	seen := make(map[byte]bool, 255)
	v := byte(1)
	for i := 0; i < 255; i++ {
		seen[v] = true
		v = gfMul(v, primitiveElement)
	}
	if len(seen) != 255 {
		t.Errorf("primitiveElement=%d generates only %d distinct nonzero elements, want 255",
			primitiveElement, len(seen))
	}
}

// TestInvertMatrixRejectsSingular verifies invertMatrix returns an error
// (not a panic, not silently wrong output) for a deliberately singular
// matrix. This path should be unreachable from any legitimate
// buildGeneratorMatrix/reconstruct call (a full-order Vandermonde matrix
// guarantees every k-subset is invertible), but nothing previously proved
// the defensive error path itself actually works — I traced this exact
// 2x2 case by hand in Python first; see the M3 review corrections note.
func TestInvertMatrixRejectsSingular(t *testing.T) {
	singular := [][]byte{ // two identical rows — rank 1, not invertible
		{1, 1},
		{1, 1},
	}
	if _, err := invertMatrix(singular, 2); err == nil {
		t.Fatal("invertMatrix: expected an error for a singular matrix, got nil")
	}
}

// TestInvertMatrixIdentity verifies invertMatrix correctly inverts the
// identity matrix to itself — the simplest non-trivial case, and a check
// that the augmented-elimination logic isn't accidentally broken for the
// trivial case while passing the harder ones.
func TestInvertMatrixIdentity(t *testing.T) {
	identity := [][]byte{{1, 0}, {0, 1}}
	got, err := invertMatrix(identity, 2)
	if err != nil {
		t.Fatalf("invertMatrix(identity): unexpected error: %v", err)
	}
	for i := range identity {
		for j := range identity[i] {
			if got[i][j] != identity[i][j] {
				t.Errorf("invertMatrix(identity)[%d][%d] = %d, want %d", i, j, got[i][j], identity[i][j])
			}
		}
	}
}