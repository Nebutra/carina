package tui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const (
	artifactPreviewPageBytes = 1 << 20
	artifactPreviewMaxBytes  = 4 << 20
)

type mediaReference struct {
	ArtifactID string
	MediaType  string
	Bytes      int64
	Origin     string
}

type artifactPreviewMsg struct {
	SessionID string
	CallID    string
	Ref       mediaReference
	Data      []byte
	Err       error
}

type artifactReadPage struct {
	NextOffset    int64  `json:"next_offset"`
	EOF           bool   `json:"eof"`
	ContentBase64 string `json:"content_base64"`
}

func mediaReferences(ev map[string]any) []mediaReference {
	if str(ev["type"]) != "ToolCallCompleted" {
		return nil
	}
	payload, _ := ev["payload"].(map[string]any)
	values, _ := payload["media_refs"].([]any)
	refs := make([]mediaReference, 0, min(len(values), 4))
	for _, value := range values {
		m, _ := value.(map[string]any)
		ref := mediaReference{ArtifactID: str(m["artifact_id"]), MediaType: str(m["media_type"]), Origin: str(m["origin"])}
		switch n := m["bytes"].(type) {
		case float64:
			ref.Bytes = int64(n)
		case int64:
			ref.Bytes = n
		case int:
			ref.Bytes = int64(n)
		}
		if len(ref.ArtifactID) == 64 && strings.HasPrefix(ref.MediaType, "image/") && ref.Bytes > 0 && ref.Bytes <= artifactPreviewMaxBytes {
			refs = append(refs, ref)
		}
		if len(refs) == 4 {
			break
		}
	}
	return refs
}

func fetchArtifactPreview(call Caller, sessionID, callID string, ref mediaReference) tea.Cmd {
	return func() tea.Msg {
		msg := artifactPreviewMsg{SessionID: sessionID, CallID: callID, Ref: ref}
		if call == nil {
			msg.Err = errors.New("artifact connection unavailable")
			return msg
		}
		data := make([]byte, 0, ref.Bytes)
		var offset int64
		for {
			var page artifactReadPage
			err := call.Call("artifact.read", map[string]any{
				"session_id": sessionID, "artifact_id": ref.ArtifactID,
				"offset": offset, "limit": artifactPreviewPageBytes,
			}, &page)
			if err != nil {
				msg.Err = err
				return msg
			}
			chunk, err := base64.StdEncoding.Strict().DecodeString(page.ContentBase64)
			if err != nil {
				msg.Err = fmt.Errorf("decode artifact: %w", err)
				return msg
			}
			if len(data)+len(chunk) > artifactPreviewMaxBytes {
				msg.Err = fmt.Errorf("artifact exceeds preview limit %d", artifactPreviewMaxBytes)
				return msg
			}
			data = append(data, chunk...)
			if page.EOF {
				break
			}
			if page.NextOffset <= offset {
				msg.Err = errors.New("artifact pagination did not advance")
				return msg
			}
			offset = page.NextOffset
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != ref.ArtifactID {
			msg.Err = errors.New("artifact digest mismatch")
			return msg
		}
		msg.Data = data
		return msg
	}
}

func (m *Model) artifactPreviewCommands(ev map[string]any) tea.Cmd {
	payload, _ := ev["payload"].(map[string]any)
	callID := str(payload["call_id"])
	refs := mediaReferences(ev)
	cmds := make([]tea.Cmd, 0, len(refs))
	for _, ref := range refs {
		cmds = append(cmds, fetchArtifactPreview(m.call, m.sessionID, callID, ref))
	}
	return tea.Batch(cmds...)
}

func (m *Model) handleArtifactPreview(msg artifactPreviewMsg) {
	if msg.SessionID != m.sessionID {
		return
	}
	p := eventPresentation{
		Key: "media:" + msg.Ref.ArtifactID, Kind: presentationFile, Status: statusSuccess,
		Title: "image", Summary: strings.TrimSpace(msg.Ref.Origin), ImageKey: msg.Ref.ArtifactID, ImageData: msg.Data,
	}
	if msg.Err != nil {
		p.Status = statusFailure
		p.Summary = "preview unavailable: " + msg.Err.Error()
		p.Body = []string{"artifact: " + msg.Ref.ArtifactID}
	}
	m.tr.upsertPresentationAfter("tool:"+msg.CallID, p, m.th, m.transcriptWidth())
	m.vp.SetContentLines(m.tr.lines)
	if m.followTail {
		m.vp.GotoBottom()
	} else {
		m.unseenLines++
	}
	m.layout()
}
