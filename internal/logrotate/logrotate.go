// Package logrotate provides a thread-safe io.Writer that rotates the
// underlying log file when it exceeds a configured size, keeping a bounded
// number of timestamped backups. Used to prevent unbounded daemon/proxy
// log growth which degrades log.Printf performance over time.
package logrotate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxSize    = 50 * 1024 * 1024
	DefaultMaxBackups = 5
	DefaultMaxAge     = 7 * 24 * time.Hour
)

type Writer struct {
	mu         sync.Mutex
	filename   string
	file       *os.File
	maxSize    int64
	maxBackups int
	maxAge     time.Duration
}

type Option func(*Writer)

func WithMaxSize(bytes int64) Option    { return func(w *Writer) { w.maxSize = bytes } }
func WithMaxBackups(n int) Option       { return func(w *Writer) { w.maxBackups = n } }
func WithMaxAge(d time.Duration) Option { return func(w *Writer) { w.maxAge = d } }

func New(filename string, opts ...Option) (*Writer, error) {
	w := &Writer{
		filename:   filename,
		maxSize:    DefaultMaxSize,
		maxBackups: DefaultMaxBackups,
		maxAge:     DefaultMaxAge,
	}
	for _, opt := range opts {
		opt(w)
	}
	if err := w.openExistingOrNew(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		if err := w.openExistingOrNew(); err != nil {
			return 0, err
		}
	}

	if info, err := w.file.Stat(); err == nil && info.Size()+int64(len(p)) > w.maxSize {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	return n, err
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeLocked()
}

func (w *Writer) closeLocked() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *Writer) openExistingOrNew() error {
	if err := os.MkdirAll(filepath.Dir(w.filename), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	w.file = f
	return nil
}

func (w *Writer) rotateLocked() error {
	w.closeLocked()

	ts := time.Now().Format("20060102-150405.000000000")
	base := strings.TrimSuffix(w.filename, ".log")
	backup := fmt.Sprintf("%s-%s.log", base, ts)
	_ = os.Rename(w.filename, backup)

	if err := w.openExistingOrNew(); err != nil {
		return err
	}
	w.cleanupOldLocked()
	return nil
}

func (w *Writer) cleanupOldLocked() {
	if w.maxBackups <= 0 && w.maxAge <= 0 {
		return
	}

	dir := filepath.Dir(w.filename)
	base := strings.TrimSuffix(filepath.Base(w.filename), ".log") + "-"

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	type backupInfo struct {
		path    string
		modTime time.Time
	}
	var backups []backupInfo
	now := time.Now()

	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupInfo{path: filepath.Join(dir, name), modTime: info.ModTime()})
	}

	sort.Slice(backups, func(i, j int) bool { return backups[i].modTime.After(backups[j].modTime) })

	for i, b := range backups {
		if w.maxBackups > 0 && i >= w.maxBackups {
			os.Remove(b.path)
			continue
		}
		if w.maxAge > 0 && now.Sub(b.modTime) > w.maxAge {
			os.Remove(b.path)
		}
	}
}
