package ivf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dim := 8
	vecs := map[uint64][]float32{
		1: makeTestVec(dim, 0.1),
		2: makeTestVec(dim, 0.5),
		3: makeTestVec(dim, 0.9),
	}
	db := testDBWithVectors(t, dim, vecs)

	idx, err := Build(db, dim, 2, 3)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "test.ivf")
	if err := idx.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not found after save: %v", err)
	}

	loaded, err := Load(path, db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.K != idx.K {
		t.Errorf("K mismatch: %d vs %d", loaded.K, idx.K)
	}
	if loaded.Dim != idx.Dim {
		t.Errorf("Dim mismatch: %d vs %d", loaded.Dim, idx.Dim)
	}
	if loaded.NProbe != idx.NProbe {
		t.Errorf("NProbe mismatch: %d vs %d", loaded.NProbe, idx.NProbe)
	}
	if loaded.TotalVectors() != idx.TotalVectors() {
		t.Errorf("TotalVectors mismatch: %d vs %d", loaded.TotalVectors(), idx.TotalVectors())
	}

	// Centroids should match
	for i := range idx.Centroids {
		for d := range idx.Centroids[i] {
			if loaded.Centroids[i][d] != idx.Centroids[i][d] {
				t.Fatalf("centroid[%d][%d] mismatch: %f vs %f", i, d, loaded.Centroids[i][d], idx.Centroids[i][d])
			}
		}
	}
}

func TestLoadInvalidMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.ivf")
	os.WriteFile(path, []byte("BAAD"), 0644)

	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("expected error for invalid magic")
	}
}

func TestLoadNonExistent(t *testing.T) {
	_, err := Load("/tmp/nonexistent-ivf-file.ivf", nil)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestAtomicSave(t *testing.T) {
	dim := 4
	vecs := map[uint64][]float32{1: makeTestVec(dim, 0.5)}
	db := testDBWithVectors(t, dim, vecs)

	idx, err := Build(db, dim, 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "atomic.ivf")
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}

	// .tmp should NOT exist (atomic rename)
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error(".tmp file should not exist after successful save")
	}
}
