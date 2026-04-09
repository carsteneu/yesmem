package ivf

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

var magic = [4]byte{'I', 'V', 'F', '1'}

// Save writes the IVF index to a binary file.
// Format: magic(4) | k(4) | dim(4) | nprobe(4) | centroids(k×dim×4) | cluster_sizes(k×4) | cluster_ids(∑sizes×8)
func (idx *IVFIndex) Save(path string) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	f, err := os.Create(path + ".tmp")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()

	// Magic
	if _, err := f.Write(magic[:]); err != nil {
		return err
	}

	// Header: k, dim, nprobe
	if err := writeUint32(f, uint32(idx.K)); err != nil {
		return err
	}
	if err := writeUint32(f, uint32(idx.Dim)); err != nil {
		return err
	}
	if err := writeUint32(f, uint32(idx.NProbe)); err != nil {
		return err
	}

	// Centroids: k × dim × float32
	for _, c := range idx.Centroids {
		for _, v := range c {
			if err := writeFloat32(f, v); err != nil {
				return err
			}
		}
	}

	// Cluster sizes: k × uint32
	for _, cluster := range idx.Clusters {
		if err := writeUint32(f, uint32(len(cluster))); err != nil {
			return err
		}
	}

	// Cluster IDs: for each cluster, size × uint64
	for _, cluster := range idx.Clusters {
		for _, id := range cluster {
			if err := writeUint64(f, id); err != nil {
				return err
			}
		}
	}

	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Load reads an IVF index from a binary file.
// Vectors are NOT loaded — they come from SQLite on demand.
func Load(path string, db *sql.DB) (*IVFIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	// Magic
	var m [4]byte
	if _, err := io.ReadFull(f, m[:]); err != nil {
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if m != magic {
		return nil, fmt.Errorf("invalid magic: %v", m)
	}

	// Header
	k, err := readUint32(f)
	if err != nil {
		return nil, fmt.Errorf("read k: %w", err)
	}
	dim, err := readUint32(f)
	if err != nil {
		return nil, fmt.Errorf("read dim: %w", err)
	}
	nprobe, err := readUint32(f)
	if err != nil {
		return nil, fmt.Errorf("read nprobe: %w", err)
	}

	// Centroids
	centroids := make([][]float32, k)
	for i := uint32(0); i < k; i++ {
		centroids[i] = make([]float32, dim)
		for d := uint32(0); d < dim; d++ {
			v, err := readFloat32(f)
			if err != nil {
				return nil, fmt.Errorf("read centroid[%d][%d]: %w", i, d, err)
			}
			centroids[i][d] = v
		}
	}

	// Cluster sizes
	sizes := make([]uint32, k)
	for i := uint32(0); i < k; i++ {
		s, err := readUint32(f)
		if err != nil {
			return nil, fmt.Errorf("read cluster size[%d]: %w", i, err)
		}
		sizes[i] = s
	}

	// Cluster IDs
	clusters := make([][]uint64, k)
	for i := uint32(0); i < k; i++ {
		clusters[i] = make([]uint64, sizes[i])
		for j := uint32(0); j < sizes[i]; j++ {
			id, err := readUint64(f)
			if err != nil {
				return nil, fmt.Errorf("read cluster[%d][%d]: %w", i, j, err)
			}
			clusters[i][j] = id
		}
	}

	return &IVFIndex{
		Centroids: centroids,
		Clusters:  clusters,
		K:         int(k),
		Dim:       int(dim),
		NProbe:    int(nprobe),
		db:        db,
	}, nil
}

// --- Binary helpers (little-endian) ---

func writeUint32(w io.Writer, v uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

func writeUint64(w io.Writer, v uint64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

func writeFloat32(w io.Writer, v float32) error {
	return writeUint32(w, math.Float32bits(v))
}

func readUint32(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func readUint64(r io.Reader) (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

func readFloat32(r io.Reader) (float32, error) {
	v, err := readUint32(r)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(v), nil
}
