// Package erasure is declared in doc.go.
// This file provides a pure-Go GF(2^8) Reed-Solomon implementation
// sufficient for the Engine interface (EncodeSegment / DecodeSegment).
//
// Field: GF(2^8) with irreducible polynomial 0x11d (= x^8 + x^4 + x^3 + x^2 + 1).
//
// Generator matrix (systematic form):
//
//	Rows 0..data-1   : identity block  [I_k]
//	Rows data..total-1: parity block    [P]
//
// The parity block P is derived from a Vandermonde matrix at points
// {α^0, α^1, …, α^(data+parity-1)} where α = 2 (primitive element of GF(2^8)).
// Using distinct powers of α guarantees that any (data×data) submatrix of the
// full encoding matrix is invertible, which is the property required for
// recovery from any data erasures.
//
// Goroutine-safety: rsEncoder is immutable after construction; all methods
// are pure (no shared mutable state).
//
// [REF: IC §5.2, ADR-003]

package erasure

import "fmt"

const (
	// primitiveElement is the primitive element of GF(2^8) used in the Vandermonde matrix.
	// See the file header documentation for field theory details.
	primitiveElement = 2
)

// ── GF(2^8) arithmetic ────────────────────────────────────────────────────────
// Irreducible polynomial: 0x11d = x^8 + x^4 + x^3 + x^2 + 1.

var gfExp [512]byte // gfExp[i] = α^i; doubled to avoid modular indexing in mul
var gfLog [256]byte // gfLog[x] = i such that α^i = x (undefined for x=0)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	// Duplicate table to simplify multiplication without modular wrap.
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// gfPow computes x^power in GF(2^8).  power must be >= 0.
// Special cases: anything^0 = 1; 0^positive = 0.
func gfPow(x byte, power int) byte {
	if power == 0 {
		return 1
	}
	if x == 0 {
		return 0
	}
	return gfExp[(int(gfLog[x])*power)%255]
}

func gfInv(x byte) byte {
	if x == 0 {
		panic("erasure: gfInv(0) is undefined")
	}
	return gfExp[255-int(gfLog[x])]
}

// gfMulTable precomputes a 256-entry lookup table for multiplication by a
// fixed factor: table[x] = gfMul(factor, x). Building the table costs 256
// gfMul calls (negligible); using it turns every subsequent multiplication
// by this factor into a single array index instead of a double log/antilog
// lookup with a zero-check branch — worthwhile since encode/reconstruct's
// inner loops apply the SAME factor across an entire 256 KB shard at a time.
// (M3 review §8.1)
func gfMulTable(factor byte) [256]byte {
	var table [256]byte
	for x := 0; x < 256; x++ {
		table[x] = gfMul(factor, byte(x))
	}
	return table
}

// ── Matrix operations over GF(2^8) ───────────────────────────────────────────

// invertMatrix computes the inverse of an n×n matrix over GF(2^8).
// Returns an error (never panics) if the matrix is singular.
func invertMatrix(m [][]byte, n int) ([][]byte, error) {
	// Augment [m | I_n]
	aug := make([][]byte, n)
	for i := range aug {
		aug[i] = make([]byte, primitiveElement*n)
		copy(aug[i], m[i])
		aug[i][n+i] = 1
	}

	for col := 0; col < n; col++ {
		// Find a non-zero pivot in column col at or below row col.
		pivot := -1
		for row := col; row < n; row++ {
			if aug[row][col] != 0 {
				pivot = row
				break
			}
		}
		if pivot < 0 {
			return nil, fmt.Errorf("erasure: matrix is singular (col %d)", col)
		}
		aug[col], aug[pivot] = aug[pivot], aug[col]

		// Scale pivot row so aug[col][col] == 1.
		scale := gfInv(aug[col][col])
		for j := 0; j < 2*n; j++ {
			aug[col][j] = gfMul(aug[col][j], scale)
		}

		// Eliminate column col from all other rows.
		for row := 0; row < n; row++ {
			if row == col || aug[row][col] == 0 {
				continue
			}
			factor := aug[row][col]
			for j := 0; j < 2*n; j++ {
				aug[row][j] ^= gfMul(factor, aug[col][j])
			}
		}
	}

	result := make([][]byte, n)
	for i := range result {
		result[i] = aug[i][n:]
	}
	return result, nil
}

// ── Generator matrix construction ────────────────────────────────────────────

// buildGeneratorMatrix returns a (parity × data) parity block in systematic form.
//
// We build the (data+parity)×data Vandermonde matrix at points
// {α^0, α^1, …, α^(data+parity-1)}, i.e. V[r][c] = (α^r)^c = α^(r·c).
// Using α=2 (primitive element), all row-evaluation points are distinct non-zero
// elements of GF(2^8), guaranteeing any k-subset of rows is invertible.
//
// To get the systematic form we multiply the full Vandermonde by the inverse of
// the top (data×data) block, then take the bottom parity rows as P.
func buildGeneratorMatrix(data, parity int) ([][]byte, error) {
	n := data + parity

	// V[r][c] = α^(r·c), where α = 2.
	vm := make([][]byte, n)
	for r := 0; r < n; r++ {
		vm[r] = make([]byte, data)
		for c := 0; c < data; c++ {
			// (α^r)^c = α^(r·c); gfPow(2, r·c) computes this correctly
			// because gfLog[2]=1, so gfExp[1·r·c % 255] = α^(r·c).
			vm[r][c] = gfPow(primitiveElement, r*c)
		}
	}

	// Invert the top data×data block.
	top := make([][]byte, data)
	copy(top, vm[:data])
	inv, err := invertMatrix(top, data)
	if err != nil {
		return nil, fmt.Errorf("erasure: Vandermonde top block is singular: %w", err)
	}

	// Multiply each parity row by inv to produce systematic parity rows.
	gen := make([][]byte, parity)
	for r := 0; r < parity; r++ {
		gen[r] = make([]byte, data)
		row := vm[data+r]
		for c := 0; c < data; c++ {
			var acc byte
			for k := 0; k < data; k++ {
				acc ^= gfMul(row[k], inv[k][c])
			}
			gen[r][c] = acc
		}
	}
	return gen, nil
}

// ── rsEncoder ─────────────────────────────────────────────────────────────────

type rsEncoder struct {
	data   int      // k — data shard count
	parity int      // r — parity shard count
	gen    [][]byte // r×k parity block of the systematic generator matrix
}

func newRSEncoder(data, parity int) (*rsEncoder, error) {
	if data < 1 || parity < 1 {
		return nil, fmt.Errorf("erasure: data=%d parity=%d: both must be >= 1", data, parity)
	}
	gen, err := buildGeneratorMatrix(data, parity)
	if err != nil {
		return nil, err
	}
	return &rsEncoder{data: data, parity: parity, gen: gen}, nil
}

// encode computes parity shards[data:] from data shards[0:data].
// Every shard must be exactly shardSize bytes.
func (enc *rsEncoder) encode(shards [][]byte, shardSize int) {
	for r, row := range enc.gen { // r = index into parity shards
		ps := shards[enc.data+r]
		for i := range ps {
			ps[i] = 0
		}
		for c, factor := range row {
			if factor == 0 {
				continue
			}
			table := gfMulTable(factor)
			ds := shards[c]
			for i := range ps {
				ps[i] ^= table[ds[i]]
			}
		}
	}
	_ = shardSize
}

// reconstruct fills nil data shards from the available shards.
// Returns ErrTooFewShards if fewer than enc.data shards are non-nil.
// All non-nil shards must be the same length; that length is used for output.
func (enc *rsEncoder) reconstruct(shards [][]byte, dataOnly bool) error {
	total := enc.data + enc.parity
	if len(shards) != total {
		return fmt.Errorf("erasure: got %d shards, need %d", len(shards), total)
	}

	// Determine shard size and count available shards.
	shardSize := 0
	nonNil := 0
	for _, s := range shards {
		if s != nil {
			if shardSize == 0 {
				shardSize = len(s)
			}
			nonNil++
		}
	}
	if nonNil < enc.data {
		return ErrTooFewShards
	}

	// Check which data shards are already present.
	allDataPresent := true
	for i := 0; i < enc.data; i++ {
		if shards[i] == nil {
			allDataPresent = false
			break
		}
	}
	if allDataPresent {
		return nil // nothing to reconstruct
	}

	// Build the (data×data) submatrix from enc.data available shards
	// and solve for the missing data shards.
	//
	// The full encoding matrix M is:
	//   Row i (i < data):   identity row i  [I_k]
	//   Row i (i >= data):  gen[i-data]     [P]
	//
	// We select any enc.data non-nil shards, form the submatrix and
	// solve M_sub · data = shards_sub for data.
	subMatrix := make([][]byte, enc.data)
	subShards := make([][]byte, enc.data)
	row := 0
	for i := 0; i < total && row < enc.data; i++ {
		if shards[i] == nil {
			continue
		}
		if i < enc.data {
			identRow := make([]byte, enc.data)
			identRow[i] = 1
			subMatrix[row] = identRow
		} else {
			subMatrix[row] = enc.gen[i-enc.data]
		}
		subShards[row] = shards[i]
		row++
	}

	invSub, err := invertMatrix(subMatrix, enc.data)
	if err != nil {
		return fmt.Errorf("erasure: submatrix inversion failed: %w", err)
	}

	// Reconstruct each missing data shard.
	for i := 0; i < enc.data; i++ {
		if shards[i] != nil {
			continue
		}
		shards[i] = make([]byte, shardSize)
		dst := shards[i]
		for src := 0; src < enc.data; src++ {
			factor := invSub[i][src]
			if factor == 0 {
				continue
			}
			table := gfMulTable(factor)
			srcShard := subShards[src]
			for b := range dst {
				dst[b] ^= table[srcShard[b]]
			}
		}
	}

	_ = dataOnly
	return nil
}
