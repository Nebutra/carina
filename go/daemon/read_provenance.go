package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nebutra/carina/go/toolchain"
)

const (
	readProvenanceVersion = 1
	maxReadSpansPerFile   = 8
)

type readProvenanceKind string

const (
	readProvenanceWhole readProvenanceKind = "whole"
	readProvenanceSpan  readProvenanceKind = "span"
)

type readProvenance struct {
	Version             int                `json:"version"`
	Kind                readProvenanceKind `json:"kind"`
	SHA256              string             `json:"sha256"`
	Content             []byte             `json:"content,omitempty"`
	StartLine           int                `json:"start_line,omitempty"`
	LineCount           int                `json:"line_count,omitempty"`
	StartByte           int64              `json:"start_byte,omitempty"`
	EndByte             int64              `json:"end_byte,omitempty"`
	EOF                 bool               `json:"eof,omitempty"`
	Truncated           bool               `json:"truncated,omitempty"`
	ObservedSize        int64              `json:"observed_size,omitempty"`
	ObservedMode        uint32             `json:"observed_mode,omitempty"`
	ObservedModTimeNano int64              `json:"observed_mod_time_unix_nano,omitempty"`
}

// sessionReadProvenance accepts the historical path->sha256 JSON shape as
// whole-file authority while emitting only the versioned record shape.
type sessionReadProvenance map[string][]readProvenance

func (p *sessionReadProvenance) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	normalized := make(sessionReadProvenance, len(raw))
	for path, value := range raw {
		var legacy string
		if json.Unmarshal(value, &legacy) == nil && legacy != "" {
			records := []readProvenance{{Version: readProvenanceVersion, Kind: readProvenanceWhole, SHA256: legacy}}
			if err := validateReadProvenance(records); err != nil {
				return fmt.Errorf("read provenance for %s: %w", path, err)
			}
			normalized[path] = records
			continue
		}
		var records []readProvenance
		if err := json.Unmarshal(value, &records); err != nil {
			return fmt.Errorf("read provenance for %s: %w", path, err)
		}
		if err := validateReadProvenance(records); err != nil {
			return fmt.Errorf("read provenance for %s: %w", path, err)
		}
		normalized[path] = cloneReadProvenance(records)
	}
	*p = normalized
	return nil
}

func validateReadProvenance(records []readProvenance) error {
	if len(records) == 0 || len(records) > maxReadSpansPerFile {
		return fmt.Errorf("requires 1-%d records", maxReadSpansPerFile)
	}
	for _, record := range records {
		if record.Version != readProvenanceVersion {
			return fmt.Errorf("unsupported version %d", record.Version)
		}
		digest, err := hex.DecodeString(record.SHA256)
		if err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("invalid sha256")
		}
		switch record.Kind {
		case readProvenanceWhole:
			if len(record.Content) != 0 || record.StartLine != 0 || record.LineCount != 0 ||
				record.StartByte != 0 || record.EndByte != 0 || record.EOF || record.Truncated ||
				record.ObservedSize != 0 || record.ObservedMode != 0 || record.ObservedModTimeNano != 0 {
				return fmt.Errorf("whole record contains span fields")
			}
		case readProvenanceSpan:
			contentBytes := int64(len(record.Content))
			if contentBytes == 0 || contentBytes > toolchain.DefaultRangeMaxOutputBytes ||
				record.StartLine < 1 || record.LineCount < 1 || record.LineCount > maxRangedReadLines ||
				record.LineCount != logicalLineCount(record.Content) || record.StartByte < 0 ||
				record.EndByte != record.StartByte+contentBytes || record.ObservedSize < record.EndByte ||
				record.EOF == record.Truncated || (record.EOF && record.EndByte != record.ObservedSize) ||
				(record.Truncated && record.EndByte >= record.ObservedSize) {
				return fmt.Errorf("invalid span bounds")
			}
			sum := sha256.Sum256(record.Content)
			if !bytes.Equal(digest, sum[:]) {
				return fmt.Errorf("span digest mismatch")
			}
		default:
			return fmt.Errorf("invalid kind %q", record.Kind)
		}
	}
	return nil
}

func cloneReadProvenance(records []readProvenance) []readProvenance {
	cloned := append([]readProvenance(nil), records...)
	for i := range cloned {
		cloned[i].Content = append([]byte(nil), records[i].Content...)
	}
	return cloned
}

func wholeReadRecord(content []byte) readProvenance {
	sum := sha256.Sum256(content)
	return readProvenance{Version: readProvenanceVersion, Kind: readProvenanceWhole, SHA256: hex.EncodeToString(sum[:])}
}

func spanReadRecord(result toolchain.LineRangeResult, version toolchain.FileVersion) (readProvenance, bool) {
	if result.EndLine < result.StartLine || len(result.Content) == 0 {
		return readProvenance{}, false
	}
	sum := sha256.Sum256(result.Content)
	return readProvenance{
		Version: readProvenanceVersion, Kind: readProvenanceSpan,
		SHA256: hex.EncodeToString(sum[:]), Content: append([]byte(nil), result.Content...),
		StartLine: result.StartLine, LineCount: result.EndLine - result.StartLine + 1,
		StartByte: result.StartByte, EndByte: result.EndByte, EOF: result.EOF, Truncated: result.Truncated,
		ObservedSize: version.Size, ObservedMode: version.Mode, ObservedModTimeNano: version.ModTimeUnixNano,
	}, true
}

func (d *Daemon) provenanceKey(sessionID, path string) (string, bool) {
	key := path
	if rel, ok := d.workspaceProvenanceKey(sessionID, path); ok {
		return rel, true
	}
	if filepath.IsAbs(filepath.Clean(path)) {
		return "", false
	}
	return key, true
}

// recordRead records whole-file authority. It intentionally replaces every
// older span because this exact content is now the complete observed file.
func (d *Daemon) recordRead(sessionID, path, content string) {
	key, ok := d.provenanceKey(sessionID, path)
	if !ok {
		return
	}
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	if d.readProv[sessionID] == nil {
		d.readProv[sessionID] = sessionReadProvenance{}
	}
	d.readProv[sessionID][key] = []readProvenance{wholeReadRecord([]byte(content))}
}

// recordRangeRead records only returned bytes. Spans accumulate only while
// the file version is unchanged; a later version starts a fresh evidence set.
func (d *Daemon) recordRangeRead(sessionID, path string, result toolchain.LineRangeResult, version toolchain.FileVersion) {
	key, ok := d.provenanceKey(sessionID, path)
	if !ok {
		return
	}
	record, hasSpan := spanReadRecord(result, version)
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	if d.readProv[sessionID] == nil {
		d.readProv[sessionID] = sessionReadProvenance{}
	}
	existing := d.readProv[sessionID][key]
	stableVersion := len(existing) > 0
	for _, prior := range existing {
		if prior.Kind != readProvenanceSpan || prior.ObservedSize != version.Size || prior.ObservedMode != version.Mode || prior.ObservedModTimeNano != version.ModTimeUnixNano {
			stableVersion = false
			break
		}
	}
	if !stableVersion {
		existing = nil
	}
	if !hasSpan {
		delete(d.readProv[sessionID], key)
		return
	}
	existing = append(existing, record)
	if len(existing) > maxReadSpansPerFile {
		existing = existing[len(existing)-maxReadSpansPerFile:]
	}
	d.readProv[sessionID][key] = cloneReadProvenance(existing)
}

func (d *Daemon) replaceWithReadSpan(sessionID, path string, span readProvenance) {
	key, ok := d.provenanceKey(sessionID, path)
	if !ok {
		return
	}
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	if d.readProv[sessionID] == nil {
		d.readProv[sessionID] = sessionReadProvenance{}
	}
	d.readProv[sessionID][key] = cloneReadProvenance([]readProvenance{span})
}

func (d *Daemon) clearReadProvenance(sessionID, path string) {
	key, ok := d.provenanceKey(sessionID, path)
	if !ok {
		return
	}
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	delete(d.readProv[sessionID], key)
}

func (d *Daemon) readProvenanceForPath(sessionID, path string) []readProvenance {
	key, _ := d.workspaceProvenanceKey(sessionID, path)
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	if records := d.readProv[sessionID][key]; len(records) > 0 {
		return cloneReadProvenance(records)
	}
	return cloneReadProvenance(d.readProv[sessionID][path])
}

// lastReadHash transfers only whole-file authority between best-of-n
// sessions. A partial candidate read can never authorize a full winner patch.
func (d *Daemon) lastReadHash(sessionID, path string) (string, bool) {
	for _, record := range d.readProvenanceForPath(sessionID, path) {
		if record.Kind == readProvenanceWhole && record.SHA256 != "" {
			return record.SHA256, true
		}
	}
	return "", false
}

// checkWriteProvenance is the complete-file patch guard. Existing files need
// a fresh whole observation; span evidence is deliberately insufficient.
func (d *Daemon) checkWriteProvenance(sessionID, relpath, abspath string) error {
	current, err := os.ReadFile(abspath)
	if err != nil {
		return nil
	}
	records := d.readProvenanceForPath(sessionID, relpath)
	wholeHash := ""
	for _, record := range records {
		if record.Kind == readProvenanceWhole {
			wholeHash = record.SHA256
			break
		}
	}
	if wholeHash == "" {
		if len(records) > 0 {
			return fmt.Errorf("refusing complete-file overwrite of %q after a partial read - read the whole file first", relpath)
		}
		return fmt.Errorf("refusing blind overwrite of existing file %q - read it first", relpath)
	}
	if wholeReadRecord(current).SHA256 != wholeHash {
		return fmt.Errorf("stale write: %q changed since you last read it - re-read before editing", relpath)
	}
	return nil
}

// materializeAuthorizedEdit returns the current-file replacement and, for a
// span-authorized edit, the updated span that must remain the only authority.
func (d *Daemon) materializeAuthorizedEdit(sessionID, path string, current []byte, old, new string) ([]byte, *readProvenance, error) {
	records := d.readProvenanceForPath(sessionID, path)
	if len(records) == 0 {
		return nil, nil, fmt.Errorf("refusing blind edit of existing file %q - read it first", path)
	}
	next, err := materializeEdit(old, new, current)
	if err != nil {
		return nil, nil, err
	}
	currentHash := wholeReadRecord(current).SHA256
	for _, record := range records {
		if record.Kind != readProvenanceWhole {
			continue
		}
		if record.SHA256 != currentHash {
			return nil, nil, fmt.Errorf("stale write: %q changed since you last read it - re-read before editing", path)
		}
		return next, nil, nil
	}

	oldBytes := []byte(old)
	covered := false
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		if record.Kind != readProvenanceSpan || bytes.Count(record.Content, oldBytes) != 1 {
			continue
		}
		covered = true
		spanOffset := bytes.Index(current, record.Content)
		if spanOffset < 0 {
			continue
		}
		updatedContent := bytes.Replace(record.Content, oldBytes, []byte(new), 1)
		sum := sha256.Sum256(updatedContent)
		record.SHA256 = hex.EncodeToString(sum[:])
		record.Content = updatedContent
		record.StartByte = int64(spanOffset)
		record.EndByte = int64(spanOffset + len(updatedContent))
		record.StartLine = 1 + bytes.Count(current[:spanOffset], []byte{'\n'})
		record.LineCount = logicalLineCount(updatedContent)
		record.EOF = spanOffset+len(record.Content) == len(next)
		record.Truncated = !record.EOF
		record.ObservedSize = int64(len(next))
		record.ObservedModTimeNano = 0
		return next, &record, nil
	}
	if covered {
		return nil, nil, fmt.Errorf("stale observed range in %q - re-read the target lines before editing", path)
	}
	return nil, nil, fmt.Errorf("edit target in %q is outside the ranges you read - read the target lines first", path)
}

func logicalLineCount(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}

func (d *Daemon) snapshotReadProvenance(sessionID string) sessionReadProvenance {
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	snapshot := make(sessionReadProvenance, len(d.readProv[sessionID]))
	for path, records := range d.readProv[sessionID] {
		snapshot[path] = cloneReadProvenance(records)
	}
	return snapshot
}

func (d *Daemon) restoreReadProvenance(sessionID string, snapshot sessionReadProvenance) {
	if snapshot == nil {
		return
	}
	d.readProvMu.Lock()
	defer d.readProvMu.Unlock()
	restored := make(sessionReadProvenance, len(snapshot))
	for path, records := range snapshot {
		restored[path] = cloneReadProvenance(records)
	}
	d.readProv[sessionID] = restored
}
