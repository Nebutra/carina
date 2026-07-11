package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/Nebutra/carina/go/artifact"
)

const maxArtifactReadBytes = 1 << 20

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
