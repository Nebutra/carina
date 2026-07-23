package tui

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	_ "golang.org/x/image/webp"

	"github.com/Nebutra/carina/go/tui/mathimage"
	"github.com/Nebutra/carina/go/tui/theme"
)

const (
	maxDraftAttachments = 4
	maxAttachmentBytes  = 4 << 20
	attachmentChunkSize = 512 << 10
	maxAttachmentSide   = 16384
	maxAttachmentPixels = 32 << 20
)

type draftAttachment struct {
	// ID is the stable editor-element identity. Digest identifies the immutable
	// content and may be shared by multiple independently editable elements.
	ID          string          `json:"id"`
	TextOffset  int             `json:"text_offset,omitempty"`
	SourcePath  string          `json:"source_path,omitempty"`
	MediaType   string          `json:"media_type"`
	ByteSize    int64           `json:"byte_size"`
	PixelWidth  int             `json:"pixel_width"`
	PixelHeight int             `json:"pixel_height"`
	Digest      string          `json:"digest"`
	Ref         *mediaReference `json:"ref,omitempty"`
	Data        []byte          `json:"-"`
}

type attachmentLoadMsg struct {
	Generation    uint64
	SessionID     string
	WorkspaceRoot string
	Text          string
	TextOffset    int
	Affinity      attachmentCaretAffinity
	InsertAfterID string
	Attachment    draftAttachment
	Err           error
}

type attachmentCaretAffinity uint8

const (
	attachmentCaretBefore attachmentCaretAffinity = iota
	attachmentCaretAfter
)

type attachmentUploadMsg struct {
	Generation    uint64
	SessionID     string
	WorkspaceRoot string
	Draft         promptDraft
	Kind          submissionKind
	Target        string
	FromQueue     bool
	ForceNew      bool
	Err           error
}

func pastedImagePath(content, workspaceRoot string) (string, bool) {
	value := strings.TrimSpace(content)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		value = unquoted
	} else {
		value = strings.Trim(value, "'\"")
	}
	if strings.HasPrefix(value, "file://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", false
		}
		value, _ = url.PathUnescape(parsed.Path)
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(workspaceRoot, value)
	}
	value = filepath.Clean(value)
	info, err := os.Stat(value)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxAttachmentBytes {
		return "", false
	}
	switch strings.ToLower(filepath.Ext(value)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return value, true
	default:
		return "", false
	}
}

func loadDraftAttachment(path, sessionID, workspaceRoot, text string, textOffset int, affinity attachmentCaretAffinity, insertAfterID string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		msg := attachmentLoadMsg{
			Generation: generation, SessionID: sessionID, WorkspaceRoot: workspaceRoot,
			Text: text, TextOffset: textOffset, Affinity: affinity, InsertAfterID: insertAfterID,
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			msg.Err = err
			return msg
		}
		attachment, err := validateDraftAttachment(path, raw)
		msg.Attachment, msg.Err = attachment, err
		return msg
	}
}

func validateDraftAttachment(path string, raw []byte) (draftAttachment, error) {
	if len(raw) < 1 || len(raw) > maxAttachmentBytes {
		return draftAttachment{}, fmt.Errorf("image must be 1..%d bytes", maxAttachmentBytes)
	}
	mediaType, ok := sniffDraftImageType(raw)
	if !ok {
		return draftAttachment{}, errors.New("allowed image types are PNG, JPEG, GIF, and WebP")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return draftAttachment{}, fmt.Errorf("decode image metadata: %w", err)
	}
	if config.Width < 1 || config.Height < 1 || config.Width > maxAttachmentSide || config.Height > maxAttachmentSide || int64(config.Width)*int64(config.Height) > maxAttachmentPixels {
		return draftAttachment{}, errors.New("image dimensions exceed the preview limit")
	}
	digest := sha256.Sum256(raw)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return draftAttachment{}, fmt.Errorf("create attachment identity: %w", err)
	}
	digestText := hex.EncodeToString(digest[:])
	return draftAttachment{
		ID: "att_" + hex.EncodeToString(idBytes), SourcePath: path, MediaType: mediaType, ByteSize: int64(len(raw)),
		PixelWidth: config.Width, PixelHeight: config.Height, Digest: digestText,
		Data: append([]byte(nil), raw...),
	}, nil
}

func sniffDraftImageType(raw []byte) (string, bool) {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "image/jpeg", true
	case len(raw) >= 6 && (bytes.Equal(raw[:6], []byte("GIF87a")) || bytes.Equal(raw[:6], []byte("GIF89a"))):
		return "image/gif", true
	case len(raw) >= 12 && bytes.Equal(raw[:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "image/webp", true
	default:
		return "", false
	}
}

func (m *Model) attachImage(path string) tea.Cmd {
	if len(m.attachments) >= maxDraftAttachments {
		m.setOperationalNoticeKind("attachment", m.text(MsgAttachmentFailed, MessageArgs{"error": fmt.Sprintf("maximum %d images", maxDraftAttachments)}), theme.RoleError)
		return nil
	}
	m.attachmentLoadGen++
	offset := m.composerCaretOffset()
	affinity := m.attachmentCaretAffinity
	insertAfterID := ""
	if m.attachmentFocus >= 0 && m.attachmentFocus < len(m.attachments) {
		offset = m.attachments[m.attachmentFocus].TextOffset
		insertAfterID = m.attachments[m.attachmentFocus].ID
	} else if affinity == attachmentCaretAfter {
		if index := m.inlineAttachmentAtCaret(-1); index >= 0 {
			insertAfterID = m.attachments[index].ID
		}
	}
	return loadDraftAttachment(path, m.sessionID, m.workspaceRoot, m.input.Value(), offset, affinity, insertAfterID, m.attachmentLoadGen)
}

func (m *Model) handleAttachmentLoad(msg attachmentLoadMsg) tea.Cmd {
	if msg.Generation != m.attachmentLoadGen || msg.SessionID != m.sessionID || cleanWorkspaceRoot(msg.WorkspaceRoot) != cleanWorkspaceRoot(m.workspaceRoot) {
		return nil
	}
	if msg.Err != nil {
		m.setOperationalNoticeKind("attachment", m.text(MsgAttachmentFailed, MessageArgs{"error": msg.Err.Error()}), theme.RoleError)
		return nil
	}
	before := m.composerSnapshot()
	msg.Attachment.TextOffset = remapInlineOffset(msg.Text, m.input.Value(), msg.TextOffset, msg.Affinity)
	if index := attachmentIndex(m.attachments, msg.InsertAfterID); index >= 0 {
		msg.Attachment.TextOffset = m.attachments[index].TextOffset
	}
	insertAt := m.attachmentInsertionIndex(msg.Attachment.TextOffset, msg.InsertAfterID, msg.Affinity)
	m.attachments = append(m.attachments, draftAttachment{})
	copy(m.attachments[insertAt+1:], m.attachments[insertAt:])
	m.attachments[insertAt] = msg.Attachment
	m.attachmentFocus = insertAt
	m.attachmentHoverID = ""
	m.attachmentCaretAffinity = attachmentCaretAfter
	m.attachmentCaretPreviewID = msg.Attachment.ID
	m.attachmentPreviewID = msg.Attachment.ID
	m.recordComposerEdit(before, composerEditPaste)
	m.layout()
	return func() tea.Msg { return attachmentGraphicsFlushMsg{} }
}

type attachmentGraphicsFlushMsg struct{}

func (m *Model) attachmentPanelLines() []string {
	if len(m.attachments) == 0 {
		return nil
	}
	lines := []string{m.th.Style(theme.RoleMuted).Render(m.text(MsgAttachmentHelp, nil))}
	for i, attachment := range m.attachments {
		name := filepath.Base(attachment.SourcePath)
		if name == "." || name == "" {
			name = attachment.MediaType
		}
		line := fmt.Sprintf("[%s %d] %s  %dx%d  %s", m.text(MsgAttachmentImage, nil), i+1, name,
			attachment.PixelWidth, attachment.PixelHeight, formatAttachmentBytes(attachment.ByteSize))
		if i == m.attachmentFocus {
			line = "> " + line
			line = m.th.Style(theme.RoleTitle).Render(line)
		} else {
			line = "  " + line
			line = m.th.Style(theme.RoleInfo).Render(line)
		}
		lines = append(lines, fitRenderedLine(line, maxInt(m.width, 1)))
	}
	return lines
}

func (m *Model) prepareAttachmentPreview() {
	preview, ok := m.previewAttachment()
	if !ok {
		if m.attachmentGraphicsOwner != "" {
			mathimage.ReleaseOwner(m.attachmentGraphicsOwner)
		}
		m.attachmentGraphicsOwner, m.attachmentGraphicsKey = "", ""
		m.attachmentPreviewLines = nil
		m.attachmentPreviewPixel = false
		return
	}
	maxWidth := maxInt(minInt(m.width-4, 56), 1)
	owner := m.graphicsOwner("composer", preview.ID)
	graphicsKey := fmt.Sprintf("%s:%d", preview.Digest, maxWidth)
	if m.attachmentGraphicsOwner != "" && (m.attachmentGraphicsOwner != owner || m.attachmentGraphicsKey != graphicsKey) {
		mathimage.ReleaseOwner(m.attachmentGraphicsOwner)
	}
	m.attachmentGraphicsOwner, m.attachmentGraphicsKey = owner, graphicsKey
	if rendered, supported := mathimage.RenderImageOwned(owner, preview.Digest, preview.Data, maxWidth, "  "); supported {
		if len(rendered) > 8 {
			rendered = rendered[:8]
		}
		m.attachmentPreviewLines = append(m.attachmentPreviewLines[:0], rendered...)
		m.attachmentPreviewPixel = true
		return
	}
	meta := fmt.Sprintf("%s · %s · %dx%d · %s · %s", m.text(MsgAttachmentPreview, nil), preview.MediaType,
		preview.PixelWidth, preview.PixelHeight, formatAttachmentBytes(preview.ByteSize), filepath.Base(preview.SourcePath))
	m.attachmentPreviewLines = []string{fitRenderedLine(m.th.Style(theme.RoleMuted).Render(meta), maxInt(m.width, 1))}
	m.attachmentPreviewPixel = false
}

func (m *Model) previewAttachment() (draftAttachment, bool) {
	for _, attachment := range m.attachments {
		if attachment.ID == m.attachmentPreviewID {
			return attachment, true
		}
	}
	return draftAttachment{}, false
}

func (m *Model) composerCaretOffset() int {
	return composerTextOffset(m.input.Value(), m.input.Line(), m.input.Column())
}

func composerTextOffset(value string, row, col int) int {
	lines := strings.Split(value, "\n")
	if len(lines) == 0 {
		return 0
	}
	row = clampInt(row, 0, len(lines)-1)
	offset := 0
	for i := 0; i < row; i++ {
		offset += len([]rune(lines[i])) + 1
	}
	return offset + clampInt(col, 0, len([]rune(lines[row])))
}

func composerRowColumn(value string, offset int) (int, int) {
	runes := []rune(value)
	offset = clampInt(offset, 0, len(runes))
	row, col := 0, 0
	for _, r := range runes[:offset] {
		if r == '\n' {
			row, col = row+1, 0
		} else {
			col++
		}
	}
	return row, col
}

func (m *Model) setComposerCaretOffset(offset int) {
	row, col := composerRowColumn(m.input.Value(), offset)
	m.setComposerCaret(row, col)
}

func remapInlineOffset(before, after string, offset int, affinity attachmentCaretAffinity) int {
	oldRunes, newRunes := []rune(before), []rune(after)
	offset = clampInt(offset, 0, len(oldRunes))
	start := 0
	for start < len(oldRunes) && start < len(newRunes) && oldRunes[start] == newRunes[start] {
		start++
	}
	oldEnd, newEnd := len(oldRunes), len(newRunes)
	for oldEnd > start && newEnd > start && oldRunes[oldEnd-1] == newRunes[newEnd-1] {
		oldEnd--
		newEnd--
	}
	if offset < start {
		return offset
	}
	if offset > oldEnd || (offset == oldEnd && oldEnd > start) {
		return clampInt(offset+(newEnd-oldEnd), 0, len(newRunes))
	}
	if oldEnd == start && offset == start && affinity == attachmentCaretAfter {
		return offset
	}
	return newEnd
}

func (m *Model) attachmentInsertionIndex(offset int, afterID string, affinity attachmentCaretAffinity) int {
	if afterID != "" {
		if index := attachmentIndex(m.attachments, afterID); index >= 0 {
			return index + 1
		}
	}
	for i, attachment := range m.attachments {
		if attachment.TextOffset > offset || (attachment.TextOffset == offset && affinity == attachmentCaretBefore) {
			return i
		}
	}
	return len(m.attachments)
}

func (m *Model) inlineAttachmentAtCaret(direction int) int {
	offset := m.composerCaretOffset()
	if direction < 0 {
		if m.attachmentCaretAffinity != attachmentCaretAfter {
			return -1
		}
		for i := len(m.attachments) - 1; i >= 0; i-- {
			if m.attachments[i].TextOffset == offset {
				return i
			}
		}
		return -1
	}
	if m.attachmentCaretAffinity != attachmentCaretBefore {
		return -1
	}
	for i := range m.attachments {
		if m.attachments[i].TextOffset == offset {
			return i
		}
	}
	return -1
}

func (m *Model) syncAttachmentPreviewOwner() bool {
	hoverID := m.attachmentHoverID
	if attachmentIndex(m.attachments, hoverID) < 0 {
		hoverID = ""
		m.attachmentHoverID = ""
	}
	keyboardID := ""
	if m.attachmentFocus >= 0 && m.attachmentFocus < len(m.attachments) {
		keyboardID = m.attachments[m.attachmentFocus].ID
	}
	if keyboardID == "" {
		direction := 1
		if m.attachmentCaretAffinity == attachmentCaretAfter {
			direction = -1
		}
		if index := m.inlineAttachmentAtCaret(direction); index >= 0 {
			keyboardID = m.attachments[index].ID
		}
	}
	m.attachmentCaretPreviewID = keyboardID
	id := hoverID
	if id == "" {
		id = keyboardID
	}
	if m.attachmentPreviewID == id {
		return false
	}
	m.attachmentPreviewID = id
	return true
}

func (m *Model) clearAttachmentInteraction() {
	m.attachmentFocus = -1
	m.attachmentCaretAffinity = attachmentCaretBefore
	m.attachmentCaretPreviewID = ""
	m.attachmentHoverID = ""
	m.attachmentPreviewID = ""
}

func (m *Model) reconcileInlineAttachments(before composerSnapshot) bool {
	oldText, newText := before.draft.Text, m.input.Value()
	oldOffset := composerTextOffset(oldText, before.row, before.col)
	newOffset := m.composerCaretOffset()
	if oldText != newText {
		for i := range m.attachments {
			m.attachments[i].TextOffset = remapInlineOffset(oldText, newText, m.attachments[i].TextOffset, before.attachmentCaretAffinity)
		}
		m.attachmentCaretAffinity = before.attachmentCaretAffinity
		for _, attachment := range m.attachments {
			if attachment.TextOffset != newOffset {
				continue
			}
			if oldAttachment := attachmentByID(before.draft.Attachments, attachment.ID); oldAttachment != nil {
				switch {
				case oldOffset > oldAttachment.TextOffset:
					m.attachmentCaretAffinity = attachmentCaretAfter
				case oldOffset < oldAttachment.TextOffset:
					m.attachmentCaretAffinity = attachmentCaretBefore
				}
			}
			break
		}
	} else if !sameInlineAttachmentElements(before.draft.Attachments, m.attachments) {
		return m.syncAttachmentPreviewOwner()
	} else if newOffset < oldOffset {
		m.attachmentCaretAffinity = attachmentCaretAfter
	} else if newOffset > oldOffset {
		m.attachmentCaretAffinity = attachmentCaretBefore
	}
	return m.syncAttachmentPreviewOwner()
}

func attachmentByID(attachments []draftAttachment, id string) *draftAttachment {
	for i := range attachments {
		if attachments[i].ID == id {
			return &attachments[i]
		}
	}
	return nil
}

func sameInlineAttachmentElements(a, b []draftAttachment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].TextOffset != b[i].TextOffset {
			return false
		}
	}
	return true
}

func (m *Model) attachmentKey(key string) (tea.Cmd, bool) {
	if len(m.attachments) == 0 {
		return nil, false
	}
	if m.attachmentFocus < 0 {
		switch {
		case key == "shift+tab":
			m.attachmentFocus = len(m.attachments) - 1
		case m.keys.matches(KeyContextEditor, ActionEditorMoveLeft, key):
			m.attachmentFocus = m.inlineAttachmentAtCaret(-1)
		case m.keys.matches(KeyContextEditor, ActionEditorMoveRight, key):
			m.attachmentFocus = m.inlineAttachmentAtCaret(1)
		case m.keys.matches(KeyContextEditor, ActionEditorDeleteBackward, key):
			if index := m.inlineAttachmentAtCaret(-1); index >= 0 {
				m.deleteInlineAttachment(index, attachmentCaretAfter)
				return nil, true
			}
			return nil, false
		case m.keys.matches(KeyContextEditor, ActionEditorDeleteForward, key):
			if index := m.inlineAttachmentAtCaret(1); index >= 0 {
				m.deleteInlineAttachment(index, attachmentCaretBefore)
				return nil, true
			}
			return nil, false
		default:
			return nil, false
		}
		if m.attachmentFocus < 0 {
			return nil, false
		}
		m.attachmentPreviewID = m.attachments[m.attachmentFocus].ID
		m.attachmentCaretPreviewID = m.attachmentPreviewID
		m.breakComposerUndoGroup()
		m.layout()
		return nil, true
	}
	selected := m.attachments[m.attachmentFocus]
	switch {
	case m.keys.matches(KeyContextEditor, ActionEditorMoveLeft, key), key == "shift+tab":
		if previous := m.attachmentFocus - 1; previous >= 0 && m.attachments[previous].TextOffset == selected.TextOffset {
			m.attachmentFocus = previous
		} else {
			m.attachmentFocus = -1
			m.attachmentCaretAffinity = attachmentCaretBefore
			m.setComposerCaretOffset(selected.TextOffset)
		}
	case m.keys.matches(KeyContextEditor, ActionEditorMoveRight, key):
		if next := m.attachmentFocus + 1; next < len(m.attachments) && m.attachments[next].TextOffset == selected.TextOffset {
			m.attachmentFocus = next
		} else {
			m.attachmentFocus = -1
			m.attachmentCaretAffinity = attachmentCaretAfter
			m.setComposerCaretOffset(selected.TextOffset)
		}
	case key == "tab", key == "esc":
		m.attachmentFocus = -1
		m.attachmentCaretAffinity = attachmentCaretAfter
		m.setComposerCaretOffset(selected.TextOffset)
		m.syncAttachmentPreviewOwner()
		m.breakComposerUndoGroup()
		m.layout()
		return nil, true
	case m.keys.matches(KeyContextEditor, ActionEditorDeleteBackward, key):
		m.deleteInlineAttachment(m.attachmentFocus, attachmentCaretAfter)
		return nil, true
	case m.keys.matches(KeyContextEditor, ActionEditorDeleteForward, key):
		m.deleteInlineAttachment(m.attachmentFocus, attachmentCaretBefore)
		return nil, true
	default:
		return nil, true
	}
	m.syncAttachmentPreviewOwner()
	m.breakComposerUndoGroup()
	m.layout()
	return nil, true
}

func (m *Model) deleteInlineAttachment(index int, affinity attachmentCaretAffinity) {
	if index < 0 || index >= len(m.attachments) {
		return
	}
	before := m.composerSnapshot()
	removed := m.attachments[index]
	m.attachments = append(m.attachments[:index], m.attachments[index+1:]...)
	m.attachmentFocus = -1
	m.attachmentHoverID = ""
	m.attachmentCaretAffinity = affinity
	m.setComposerCaretOffset(removed.TextOffset)
	m.syncAttachmentPreviewOwner()
	m.recordComposerEdit(before, composerEditOther)
	m.layout()
}

func attachmentIndex(attachments []draftAttachment, id string) int {
	for i := range attachments {
		if attachments[i].ID == id {
			return i
		}
	}
	return -1
}

func formatAttachmentBytes(size int64) string {
	if size >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
	}
	if size >= 1<<10 {
		return fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
	}
	return fmt.Sprintf("%d B", size)
}

func draftNeedsAttachmentUpload(draft promptDraft) bool {
	for _, attachment := range draft.Attachments {
		if attachment.Ref == nil {
			return true
		}
	}
	return false
}

func draftMediaReferences(draft promptDraft) []mediaReference {
	refs := make([]mediaReference, 0, len(draft.Attachments))
	for _, attachment := range draft.Attachments {
		if attachment.Ref != nil {
			refs = append(refs, *attachment.Ref)
		}
	}
	return refs
}

func (m *Model) beginAttachmentUpload(kind submissionKind, target string, draft promptDraft, fromQueue, forceNew bool) tea.Cmd {
	if m.attachmentUploadBusy {
		return nil
	}
	m.attachmentUploadBusy = true
	m.attachmentUploadGen++
	generation := m.attachmentUploadGen
	call, sessionID, workspaceRoot := m.call, m.sessionID, m.workspaceRoot
	draft = cloneDraft(draft)
	return func() tea.Msg {
		msg := attachmentUploadMsg{
			Generation: generation, SessionID: sessionID, WorkspaceRoot: workspaceRoot,
			Draft: draft, Kind: kind, Target: target, FromQueue: fromQueue, ForceNew: forceNew,
		}
		if call == nil {
			msg.Err = errors.New("daemon not connected")
			return msg
		}
		uploadedByDigest := make(map[string]mediaReference, len(msg.Draft.Attachments))
		for i := range msg.Draft.Attachments {
			attachment := &msg.Draft.Attachments[i]
			if attachment.Ref != nil {
				uploadedByDigest[attachment.Digest] = *attachment.Ref
				continue
			}
			if ref, ok := uploadedByDigest[attachment.Digest]; ok {
				attachment.Ref = &ref
				continue
			}
			if len(attachment.Data) == 0 && attachment.SourcePath != "" {
				raw, err := os.ReadFile(attachment.SourcePath)
				if err != nil {
					msg.Err = err
					return msg
				}
				validated, err := validateDraftAttachment(attachment.SourcePath, raw)
				if err != nil || validated.Digest != attachment.Digest {
					if err == nil {
						err = errors.New("image changed after it was attached")
					}
					msg.Err = err
					return msg
				}
				attachment.Data = validated.Data
			}
			ref, err := uploadAttachment(call, sessionID, generation, i, *attachment)
			if err != nil {
				msg.Err = err
				return msg
			}
			attachment.Ref = &ref
			uploadedByDigest[attachment.Digest] = ref
		}
		return msg
	}
}

func uploadAttachment(call Caller, sessionID string, generation uint64, position int, attachment draftAttachment) (mediaReference, error) {
	uploadID := fmt.Sprintf("tui-%d-%d-%s", generation, position, attachment.Digest[:12])
	chunks := (len(attachment.Data) + attachmentChunkSize - 1) / attachmentChunkSize
	var ref mediaReference
	for index := 0; index < chunks; index++ {
		start := index * attachmentChunkSize
		end := minInt(start+attachmentChunkSize, len(attachment.Data))
		params := map[string]any{
			"session_id": sessionID, "upload_id": uploadID, "chunk_index": index,
			"content_base64": base64.StdEncoding.EncodeToString(attachment.Data[start:end]),
			"final":          index == chunks-1, "sha256": attachment.Digest,
			"total_bytes": attachment.ByteSize, "media_type": attachment.MediaType,
			"origin": fmt.Sprintf("composer image %d", position+1),
		}
		if err := call.Call("artifact.upload", params, &ref); err != nil {
			return mediaReference{}, err
		}
	}
	if ref.ArtifactID == "" || ref.ArtifactID != attachment.Digest {
		return mediaReference{}, errors.New("artifact upload returned an invalid digest")
	}
	return ref, nil
}

func (m *Model) handleAttachmentUpload(msg attachmentUploadMsg) tea.Cmd {
	if msg.Generation != m.attachmentUploadGen || msg.SessionID != m.sessionID || cleanWorkspaceRoot(msg.WorkspaceRoot) != cleanWorkspaceRoot(m.workspaceRoot) {
		return nil
	}
	m.attachmentUploadBusy = false
	if msg.Err != nil {
		m.setOperationalNoticeKind("attachment", m.text(MsgAttachmentFailed, MessageArgs{"error": msg.Err.Error()}), theme.RoleError)
		return nil
	}
	for i := range m.attachments {
		for _, uploaded := range msg.Draft.Attachments {
			if m.attachments[i].ID == uploaded.ID {
				m.attachments[i].Ref = uploaded.Ref
			}
		}
	}
	return m.beginSubmissionSourceWithIntent(msg.Kind, msg.Target, msg.Draft, msg.FromQueue, msg.ForceNew)
}
