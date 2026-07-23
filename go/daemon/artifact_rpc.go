package daemon

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/artifact"
)

const (
	maxArtifactReadBytes   = 1 << 20
	maxArtifactUploadChunk = 512 << 10
	maxArtifactUploadBytes = 4 << 20
	maxArtifactUploads     = 8
	artifactUploadTTL      = 5 * time.Minute
)

type artifactUploadState struct {
	SessionID  string
	UploadID   string
	Digest     string
	MediaType  string
	Origin     string
	TotalBytes int64
	NextChunk  int
	Content    []byte
	UpdatedAt  time.Time
}

type artifactUploadParams struct {
	SessionID     string `json:"session_id"`
	UploadID      string `json:"upload_id"`
	ChunkIndex    int    `json:"chunk_index"`
	ContentBase64 string `json:"content_base64"`
	Final         bool   `json:"final"`
	SHA256        string `json:"sha256"`
	TotalBytes    int64  `json:"total_bytes"`
	MediaType     string `json:"media_type"`
	Origin        string `json:"origin"`
}

type artifactParams struct {
	SessionID  string `json:"session_id"`
	TaskID     string `json:"task_id"`
	CallID     string `json:"call_id"`
	ArtifactID string `json:"artifact_id"`
	Offset     int64  `json:"offset"`
	Limit      int64  `json:"limit"`
}

func decodeArtifactParams(raw json.RawMessage) (artifactParams, error) {
	var p artifactParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" || p.ArtifactID == "" {
		return p, fmt.Errorf("session_id and artifact_id are required")
	}
	if p.Offset < 0 || p.Limit < 0 {
		return p, fmt.Errorf("offset and limit must be non-negative")
	}
	if p.Limit == 0 {
		p.Limit = 64 << 10
	}
	if p.Limit > maxArtifactReadBytes {
		return p, fmt.Errorf("limit must not exceed %d", maxArtifactReadBytes)
	}
	return p, nil
}

func (p artifactParams) scope() artifact.Scope {
	return artifact.Scope{SessionID: p.SessionID, TaskID: p.TaskID, CallID: p.CallID}
}

func (d *Daemon) handleArtifactStat(raw json.RawMessage) (any, error) {
	p, err := decodeArtifactParams(raw)
	if err != nil {
		return nil, err
	}
	return d.artifacts.Stat(p.scope(), p.ArtifactID)
}

func (d *Daemon) handleArtifactRead(raw json.RawMessage) (any, error) {
	p, err := decodeArtifactParams(raw)
	if err != nil {
		return nil, err
	}
	content, meta, err := d.artifacts.Read(p.scope(), p.ArtifactID)
	if err != nil {
		return nil, err
	}
	start := p.Offset
	if start > int64(len(content)) {
		start = int64(len(content))
	}
	end := start + p.Limit
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return map[string]any{
		"metadata": meta, "offset": start, "next_offset": end, "eof": end == int64(len(content)),
		"content_base64": base64.StdEncoding.EncodeToString(content[start:end]),
	}, nil
}

func (d *Daemon) handleArtifactUpload(raw json.RawMessage) (any, error) {
	var p artifactUploadParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" || !validClientSubmissionID(p.UploadID) {
		return nil, fmt.Errorf("session_id and a valid upload_id are required")
	}
	if p.ChunkIndex < 0 || p.TotalBytes < 1 || p.TotalBytes > maxArtifactUploadBytes {
		return nil, fmt.Errorf("invalid upload chunk index or total_bytes")
	}
	if len(p.SHA256) != 64 || strings.TrimSpace(p.MediaType) == "" {
		return nil, fmt.Errorf("sha256 and media_type are required")
	}
	if sess, ok := d.store.Get(p.SessionID); !ok || sess.Status != "active" {
		return nil, fmt.Errorf("unknown or inactive session %s", p.SessionID)
	}
	chunk, err := base64.StdEncoding.Strict().DecodeString(p.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("invalid content_base64: %w", err)
	}
	if len(chunk) < 1 || len(chunk) > maxArtifactUploadChunk {
		return nil, fmt.Errorf("upload chunks must be 1..%d bytes", maxArtifactUploadChunk)
	}

	now := time.Now()
	key := p.SessionID + "\x00" + p.UploadID
	d.artifactUploadMu.Lock()
	defer d.artifactUploadMu.Unlock()
	if d.artifactUploads == nil {
		d.artifactUploads = make(map[string]*artifactUploadState)
	}
	for id, upload := range d.artifactUploads {
		if now.Sub(upload.UpdatedAt) > artifactUploadTTL {
			delete(d.artifactUploads, id)
		}
	}
	upload := d.artifactUploads[key]
	if upload == nil {
		active := 0
		for _, candidate := range d.artifactUploads {
			if candidate.SessionID == p.SessionID {
				active++
			}
		}
		if active >= maxArtifactUploads {
			return nil, fmt.Errorf("too many active artifact uploads")
		}
		upload = &artifactUploadState{
			SessionID: p.SessionID, UploadID: p.UploadID, Digest: strings.ToLower(p.SHA256),
			MediaType: p.MediaType, Origin: p.Origin, TotalBytes: p.TotalBytes,
			Content: make([]byte, 0, p.TotalBytes), UpdatedAt: now,
		}
		d.artifactUploads[key] = upload
	}
	if upload.NextChunk != p.ChunkIndex {
		return nil, fmt.Errorf("upload chunk out of order: got %d want %d", p.ChunkIndex, upload.NextChunk)
	}
	if upload.Digest != strings.ToLower(p.SHA256) || upload.MediaType != p.MediaType || upload.TotalBytes != p.TotalBytes || upload.Origin != p.Origin {
		return nil, fmt.Errorf("upload metadata changed between chunks")
	}
	if int64(len(upload.Content)+len(chunk)) > upload.TotalBytes {
		delete(d.artifactUploads, key)
		return nil, fmt.Errorf("upload exceeds declared total_bytes")
	}
	upload.Content = append(upload.Content, chunk...)
	upload.NextChunk++
	upload.UpdatedAt = now
	if !p.Final {
		return map[string]any{"upload_id": p.UploadID, "next_chunk_index": upload.NextChunk}, nil
	}
	delete(d.artifactUploads, key)
	if int64(len(upload.Content)) != upload.TotalBytes {
		return nil, fmt.Errorf("final upload size mismatch")
	}
	digest := sha256.Sum256(upload.Content)
	if fmt.Sprintf("%x", digest[:]) != upload.Digest {
		return nil, fmt.Errorf("artifact digest mismatch")
	}
	ref, err := ingestImageMedia(d.artifacts, artifact.Scope{SessionID: p.SessionID}, upload.Origin, upload.Content)
	if err != nil {
		return nil, err
	}
	if ref.MediaType != upload.MediaType {
		return nil, fmt.Errorf("declared media_type %q does not match content %q", upload.MediaType, ref.MediaType)
	}
	return ref, nil
}
