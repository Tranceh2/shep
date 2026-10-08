// Package queryhistory keeps the picker's recent searches: the queries that
// ended in a selection, newest first, for ctrl+y to bring back. They live in
// a small text file, one query per line, rewritten atomically.
package queryhistory

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Limit is how many queries the history keeps.
const Limit = 50

// Load returns the saved queries, newest first. A missing file is an empty
// history.
func Load(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var queries []string
	for line := range strings.Lines(string(data)) {
		if q := strings.TrimRight(line, "\n"); q != "" {
			queries = append(queries, q)
		}
	}
	return queries, nil
}

// Clear forgets every saved query. A missing file is already clear.
func Clear(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Record saves query as the newest entry, dropping its older copy and
// anything past Limit. A blank query is not saved.
func Record(path, query string) error {
	query = strings.ReplaceAll(query, "\n", " ")
	if strings.TrimSpace(query) == "" {
		return nil
	}
	queries, err := Load(path)
	if err != nil {
		return err
	}
	queries = slices.DeleteFunc(queries, func(q string) bool { return q == query })
	queries = append([]string{query}, queries[:min(len(queries), Limit-1)]...)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".queries-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(strings.Join(queries, "\n") + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
