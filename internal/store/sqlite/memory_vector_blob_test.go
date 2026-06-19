// memory_vector_blob_test.go — unit coverage for vectorFromBlob, the decoder
// that turns sqlite-vec's packed little-endian float32 blob back into a
// []float32. These tests pin the binary-decode path (including a vector whose
// first encoded byte is 0x5B, the ASCII '[' that a content-byte heuristic
// would have misclassified as JSON) and the dimension-mismatch error path.
package sqlite

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// packVec encodes a []float32 the way sqlite-vec stores a FLOAT[N] column:
// packed little-endian, 4 bytes per element.
func packVec(v []float32) []byte {
	out := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[i*4:i*4+4], math.Float32bits(f))
	}
	return out
}

// TestVectorFromBlob_BinaryRoundtrip decodes a full-dimension packed blob and
// confirms every element survives. The vector is crafted so its FIRST encoded
// byte is 0x5B ('['): the old looksLikeJSONArray heuristic would have routed
// this binary blob into the JSON parser and failed. With the heuristic gone,
// the binary path handles it.
func TestVectorFromBlob_BinaryRoundtrip(t *testing.T) {
	want := make([]float32, memoryVecDim)
	for i := range want {
		want[i] = float32(i%17) * 0.013
	}
	// Craft element 0 so its little-endian low byte is 0x5B ('['). We search
	// for a float32 whose bit pattern has 0x5B in the least-significant byte.
	var first float32
	found := false
	for bits := uint32(0); bits < 1<<16; bits++ {
		if byte(bits&0xFF) == 0x5B {
			first = math.Float32frombits(bits)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("could not craft a float32 with low byte 0x5B")
	}
	want[0] = first

	raw := packVec(want)
	if raw[0] != 0x5B {
		t.Fatalf("setup: first encoded byte = %#x, want 0x5B", raw[0])
	}

	got, err := vectorFromBlob(raw)
	if err != nil {
		t.Fatalf("vectorFromBlob: %v", err)
	}
	if len(got) != memoryVecDim {
		t.Fatalf("len(got) = %d, want %d", len(got), memoryVecDim)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestVectorFromBlob_DimMismatch confirms a binary blob whose length is a
// multiple of 4 but does NOT decode to the column dimension is rejected with a
// clear error rather than silently returning a wrong-sized vector.
func TestVectorFromBlob_DimMismatch(t *testing.T) {
	short := packVec(make([]float32, 4)) // 4 floats, %4==0, but != memoryVecDim
	_, err := vectorFromBlob(short)
	if err == nil {
		t.Fatal("expected a dimension-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "want 1536") {
		t.Errorf("error should name the expected dim, got %q", err.Error())
	}
}

// TestVectorFromBlob_Empty rejects an empty blob.
func TestVectorFromBlob_Empty(t *testing.T) {
	if _, err := vectorFromBlob(nil); err == nil {
		t.Fatal("expected error on empty blob")
	}
}

// TestVectorFromBlob_JSONFallback confirms the JSON-text fallback fires ONLY
// when the length is not a multiple of 4 (impossible for a packed float32
// blob). A JSON array is padded here so its byte length %4 != 0.
func TestVectorFromBlob_JSONFallback(t *testing.T) {
	raw := []byte("[0.1,0.2,0.3]") // 13 bytes → %4 == 1 → JSON path
	if len(raw)%4 == 0 {
		t.Fatalf("setup: JSON blob length %d is a multiple of 4; adjust the test", len(raw))
	}
	got, err := vectorFromBlob(raw)
	if err != nil {
		t.Fatalf("JSON fallback: %v", err)
	}
	want := []float32{0.1, 0.2, 0.3}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
