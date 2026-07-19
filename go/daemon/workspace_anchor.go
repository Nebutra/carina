package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/continuity"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func (d *Daemon) captureWorkspaceAnchor(sess *sessionstore.Session) (*continuity.WorkspaceAnchor, error) {
	realRoot, err := filepath.EvalSymlinks(sess.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return nil, err
	}
	d.readProvMu.Lock()
	dependencies := make([]string, 0, len(d.readProv[sess.SessionID]))
	readHashes := make(map[string]string, len(d.readProv[sess.SessionID]))
	for path, hash := range d.readProv[sess.SessionID] {
		dependencies = append(dependencies, path)
		readHashes[path] = hash
	}
	d.readProvMu.Unlock()
	mutations := []string{}
	patches, patchErr := d.kern.PatchList(sess.SessionID)
	if patchErr != nil {
		return nil, fmt.Errorf("load patch lineage: %w", patchErr)
	}
	if patches != nil {
		seen := map[string]bool{}
		for _, patch := range patches {
			if patch.Status != "applied" && patch.Status != "committed" {
				continue
			}
			for _, path := range patch.AffectedFiles {
				if !seen[path] {
					seen[path], mutations = true, append(mutations, path)
				}
			}
		}
	}
	sort.Strings(dependencies)
	sort.Strings(mutations)
	dependencyFiles, err := digestWorkspaceFiles(realRoot, dependencies)
	if err != nil {
		return nil, err
	}
	for _, file := range dependencyFiles {
		if expected := readHashes[file.Path]; expected == "" || file.SHA256 != expected {
			return nil, fmt.Errorf("workspace dependency drifted since read: %s", file.Path)
		}
	}
	mutationFiles, err := digestWorkspaceFiles(realRoot, mutations)
	if err != nil {
		return nil, err
	}
	anchor := &continuity.WorkspaceAnchor{
		WorkspaceRealpath: realRoot, DependencyFiles: dependencyFiles,
		MutationFiles: mutationFiles, PatchLineage: d.appliedPatchIDs(sess), CreatedAt: time.Now().UTC(),
	}
	identity, _ := json.Marshal(anchor)
	sum := sha256.Sum256(identity)
	anchor.ID = "anchor_" + hex.EncodeToString(sum[:16])
	return anchor, anchor.Validate()
}

const maxAnchorFileBytes = 16 << 20
const maxAnchorTotalBytes = 64 << 20

func digestWorkspaceFiles(root string, paths []string) ([]continuity.FileDigest, error) {
	out := make([]continuity.FileDigest, 0, len(paths))
	var total int64
	for _, path := range paths {
		clean := filepath.Clean(path)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("workspace anchor path escapes root: %s", path)
		}
		abs := filepath.Join(root, clean)
		resolved, resolveErr := filepath.EvalSymlinks(abs)
		if os.IsNotExist(resolveErr) {
			out = append(out, continuity.FileDigest{Path: clean, SHA256: "missing"})
			continue
		}
		if resolveErr != nil || !pathWithin(root, resolved) {
			return nil, fmt.Errorf("workspace anchor path is inaccessible or escapes through symlink: %s", clean)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("workspace anchor dependency is not a regular file: %s", clean)
		}
		if info.Size() > maxAnchorFileBytes || total+info.Size() > maxAnchorTotalBytes {
			return nil, fmt.Errorf("workspace anchor hashing budget exceeded at %s", clean)
		}
		raw, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read workspace anchor dependency %s: %w", clean, err)
		}
		total += int64(len(raw))
		sum := sha256.Sum256(raw)
		out = append(out, continuity.FileDigest{Path: clean, Mode: uint32(info.Mode()), Bytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

func verifyWorkspaceAnchor(anchor *continuity.WorkspaceAnchor) (bool, string) {
	if anchor == nil {
		return false, "checkpoint has no workspace anchor"
	}
	realRoot, err := filepath.EvalSymlinks(anchor.WorkspaceRealpath)
	if err != nil || realRoot != anchor.WorkspaceRealpath {
		return false, "workspace identity changed"
	}
	for _, expected := range append(append([]continuity.FileDigest(nil), anchor.DependencyFiles...), anchor.MutationFiles...) {
		files, err := digestWorkspaceFiles(anchor.WorkspaceRealpath, []string{expected.Path})
		if err != nil || len(files) != 1 {
			return false, "workspace dependency cannot be verified: " + expected.Path
		}
		actual := files[0]
		if actual.Mode != expected.Mode || actual.Bytes != expected.Bytes || actual.SHA256 != expected.SHA256 {
			return false, "workspace drift: " + expected.Path
		}
	}
	return true, "workspace anchor matches"
}
