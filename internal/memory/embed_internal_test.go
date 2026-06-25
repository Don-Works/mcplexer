// embed_internal_test.go — white-box coverage for the dimension-fitting
// that makes local (sub-1536-dim) embedding models usable.
package memory

import "testing"

func TestFitToEmbedDim(t *testing.T) {
	// Exact width: passed through.
	exact := make([]float32, EmbedDim)
	exact[0], exact[EmbedDim-1] = 1, 2
	got, err := fitToEmbedDim(exact)
	if err != nil || len(got) != EmbedDim || got[0] != 1 || got[EmbedDim-1] != 2 {
		t.Fatalf("exact: got len=%d err=%v", len(got), err)
	}

	// Smaller width (the common local case): zero-padded, prefix preserved.
	small := []float32{0.5, -0.25, 0.75} // 3-dim stand-in for 768/1024
	got, err = fitToEmbedDim(small)
	if err != nil {
		t.Fatalf("small: unexpected err %v", err)
	}
	if len(got) != EmbedDim {
		t.Fatalf("small: padded len=%d, want %d", len(got), EmbedDim)
	}
	for i, want := range small {
		if got[i] != want {
			t.Fatalf("small: prefix[%d]=%v, want %v", i, got[i], want)
		}
	}
	for i := len(small); i < EmbedDim; i++ {
		if got[i] != 0 {
			t.Fatalf("small: pad[%d]=%v, want 0", i, got[i])
		}
	}

	// Empty: rejected.
	if _, err := fitToEmbedDim(nil); err == nil {
		t.Fatal("empty vector should error")
	}

	// Wider than EmbedDim: rejected (truncation would break cosine).
	if _, err := fitToEmbedDim(make([]float32, EmbedDim+1)); err == nil {
		t.Fatal("oversized vector should error")
	}
}

// TestZeroPadPreservesCosine is the correctness anchor for the padding
// trick: cosine(pad(a), pad(b)) must equal cosine(a, b).
func TestZeroPadPreservesCosine(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{4, 5, 6}
	pa, _ := fitToEmbedDim(a)
	pb, _ := fitToEmbedDim(b)

	cos := func(x, y []float32) float64 {
		var dot, nx, ny float64
		for i := range x {
			dot += float64(x[i]) * float64(y[i])
			nx += float64(x[i]) * float64(x[i])
			ny += float64(y[i]) * float64(y[i])
		}
		if nx == 0 || ny == 0 {
			return 0
		}
		return dot / (sqrt(nx) * sqrt(ny))
	}
	orig := cos(a, b)
	padded := cos(pa, pb)
	if diff := orig - padded; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cosine not preserved: orig=%v padded=%v", orig, padded)
	}
}

// sqrt avoids pulling math into the test for one call.
func sqrt(x float64) float64 {
	if x == 0 {
		return 0
	}
	z := x
	for i := 0; i < 40; i++ {
		z = (z + x/z) / 2
	}
	return z
}
