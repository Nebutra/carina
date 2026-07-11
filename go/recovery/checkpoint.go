// Package recovery implements user-visible, fail-closed workspace checkpoints.
package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type File struct {
	Path, Hash string
	Mode       uint32
	Content    []byte
}
type Checkpoint struct {
	ID, SessionID, Summary string
	CreatedAt              time.Time
	Files                  []File
}
type Change struct{ Path, Action string }

type Store struct{ Dir string }

func (s Store) Create(sessionID, root, summary string) (Checkpoint, error) {
	if sessionID == "" {
		return Checkpoint{}, errors.New("session id is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Checkpoint{}, err
	}
	cp := Checkpoint{SessionID: sessionID, Summary: strings.TrimSpace(summary), CreatedAt: time.Now().UTC()}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel == ".git" || strings.HasPrefix(rel, ".carina") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		cp.Files = append(cp.Files, File{Path: filepath.ToSlash(rel), Hash: hex.EncodeToString(sum[:]), Mode: uint32(info.Mode().Perm()), Content: raw})
		return nil
	})
	if err != nil {
		return Checkpoint{}, err
	}
	sort.Slice(cp.Files, func(i, j int) bool { return cp.Files[i].Path < cp.Files[j].Path })
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", sessionID, cp.Summary, cp.CreatedAt.UnixNano())))
	cp.ID = hex.EncodeToString(h[:8])
	return cp, s.write(cp)
}

func (s Store) write(cp Checkpoint) error {
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(s.Dir, cp.ID+".json")
	tmp := p + ".tmp"
	if err = os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
func (s Store) Load(id string) (Checkpoint, error) {
	if id == "" || filepath.Base(id) != id {
		return Checkpoint{}, errors.New("invalid checkpoint id")
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if err != nil {
		return Checkpoint{}, err
	}
	var cp Checkpoint
	if err = json.Unmarshal(raw, &cp); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}
func (s Store) List(sessionID string) ([]Checkpoint, error) {
	es, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return []Checkpoint{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Checkpoint{}
	for _, e := range es {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		cp, err := s.Load(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil && (sessionID == "" || cp.SessionID == sessionID) {
			for i := range cp.Files {
				cp.Files[i].Content = nil
			}
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func Preview(cp Checkpoint, root string) ([]Change, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	wanted := map[string]File{}
	for _, f := range cp.Files {
		wanted[f.Path] = f
	}
	changes := []Change{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || strings.HasPrefix(rel, ".carina") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(raw)
		f, ok := wanted[rel]
		if !ok {
			changes = append(changes, Change{rel, "delete"})
		} else if hex.EncodeToString(sum[:]) != f.Hash {
			changes = append(changes, Change{rel, "restore"})
		}
		delete(wanted, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for p := range wanted {
		changes = append(changes, Change{p, "create"})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}
func Restore(cp Checkpoint, root string, confirmed bool) error {
	if !confirmed {
		return errors.New("restore is destructive; pass explicit confirmation")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	changes, err := Preview(cp, root)
	if err != nil {
		return err
	}
	byPath := map[string]File{}
	for _, f := range cp.Files {
		byPath[f.Path] = f
	}
	for _, c := range changes {
		p := filepath.Join(root, filepath.FromSlash(c.Path))
		if c.Action == "delete" {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		f := byPath[c.Path]
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.Content, fs.FileMode(f.Mode)); err != nil {
			return err
		}
	}
	return nil
}
