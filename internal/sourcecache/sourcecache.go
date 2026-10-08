// Package sourcecache saves the last result of the slow sources (the projects
// scan, custom source commands), so the picker can show it the moment it
// opens while the source runs again in the background. Each source has one
// JSON file; an entry saved under another configuration of the source, or by
// another schema, is ignored.
package sourcecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/tranceh2/shep/internal/source"
)

// schema versions the file layout; an entry of another schema is ignored.
const schema = 1

// Result is a source's saved candidates with the normalized form of their
// paths (see resolver.NormalizedPaths), so showing them reads no directory.
type Result struct {
	Candidates      []source.Candidate
	NormalizedPaths map[string]string
}

// entry is the file layout. Candidates are stored without their resolved
// presentation, which the reader attaches again.
type entry struct {
	Schema          int               `json:"schema"`
	Fingerprint     string            `json:"fingerprint"`
	Candidates      []candidate       `json:"candidates"`
	NormalizedPaths map[string]string `json:"normalized_paths,omitempty"`
}

type candidate struct {
	Path           string            `json:"path,omitempty"`
	NormalizedPath string            `json:"normalized_path,omitempty"`
	Label          string            `json:"label,omitempty"`
	Icon           string            `json:"icon,omitempty"`
	Source         string            `json:"source,omitempty"`
	Aliases        []string          `json:"aliases,omitempty"`
	Meta           map[string]string `json:"meta,omitempty"`
}

// Fingerprint identifies a source's configuration: the hash of its JSON
// encoding.
func Fingerprint(config any) string {
	data, _ := json.Marshal(config)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Load returns the result saved for the source name in dir under
// fingerprint; ok is false when there is none.
func Load(dir, name, fingerprint string) (Result, bool) {
	data, err := os.ReadFile(path(dir, name))
	if err != nil {
		return Result{}, false
	}
	var e entry
	if json.Unmarshal(data, &e) != nil || e.Schema != schema || e.Fingerprint != fingerprint {
		return Result{}, false
	}
	cands := make([]source.Candidate, len(e.Candidates))
	for i, c := range e.Candidates {
		cands[i] = source.Candidate{
			Path: c.Path, NormalizedPath: c.NormalizedPath, Label: c.Label, Icon: c.Icon,
			Source: c.Source, Aliases: c.Aliases, Meta: c.Meta,
		}
	}
	return Result{Candidates: cands, NormalizedPaths: e.NormalizedPaths}, true
}

// Save replaces the result saved for the source name in dir, atomically.
func Save(dir, name, fingerprint string, r Result) error {
	e := entry{Schema: schema, Fingerprint: fingerprint, NormalizedPaths: r.NormalizedPaths, Candidates: make([]candidate, len(r.Candidates))}
	for i, c := range r.Candidates {
		e.Candidates[i] = candidate{
			Path: c.Path, NormalizedPath: c.NormalizedPath, Label: c.Label, Icon: c.Icon,
			Source: c.Source, Aliases: c.Aliases, Meta: c.Meta,
		}
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path(dir, name))
}

// path is the source's file; the name is hashed so any custom source name
// is a safe file name.
func path(dir, name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".json")
}
