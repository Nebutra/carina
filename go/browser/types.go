package browser

import (
	"context"
	"fmt"
	"strings"
)

type Mode string

const (
	ModeManaged Mode = "managed"
	ModeAttach  Mode = "attach"
)

type SessionKey struct {
	TenantID  string
	SessionID string
}

func (k SessionKey) validate() error {
	if strings.TrimSpace(k.TenantID) == "" || strings.TrimSpace(k.SessionID) == "" {
		return browserError(ErrorInvalidRequest, "tenant and session are required", "provide the owning tenant and session", false, nil)
	}
	if len(k.TenantID) > 256 || len(k.SessionID) > 256 || strings.ContainsRune(k.TenantID+k.SessionID, '\x00') {
		return browserError(ErrorInvalidRequest, "tenant or session identity is invalid", "use bounded text identifiers", false, nil)
	}
	return nil
}

type Limits struct {
	MaxTabs            int
	MaxSnapshotNodes   int
	MaxSnapshotDepth   int
	MaxSnapshotChars   int
	MaxNodeFieldChars  int
	MaxScreenshotBytes int
	MaxDownloadBytes   int
}

func DefaultLimits() Limits {
	return Limits{
		MaxTabs:            32,
		MaxSnapshotNodes:   1_000,
		MaxSnapshotDepth:   24,
		MaxSnapshotChars:   128 << 10,
		MaxNodeFieldChars:  4 << 10,
		MaxScreenshotBytes: 8 << 20,
		MaxDownloadBytes:   8 << 20,
	}
}

func (l Limits) normalized() Limits {
	d := DefaultLimits()
	if l.MaxTabs > 0 {
		d.MaxTabs = l.MaxTabs
	}
	if l.MaxSnapshotNodes > 0 {
		d.MaxSnapshotNodes = l.MaxSnapshotNodes
	}
	if l.MaxSnapshotDepth > 0 {
		d.MaxSnapshotDepth = l.MaxSnapshotDepth
	}
	if l.MaxSnapshotChars > 0 {
		d.MaxSnapshotChars = l.MaxSnapshotChars
	}
	if l.MaxNodeFieldChars > 0 {
		d.MaxNodeFieldChars = l.MaxNodeFieldChars
	}
	if l.MaxScreenshotBytes > 0 {
		d.MaxScreenshotBytes = l.MaxScreenshotBytes
	}
	if l.MaxDownloadBytes > 0 {
		d.MaxDownloadBytes = l.MaxDownloadBytes
	}
	return d
}

type LaunchOptions struct {
	BrowserID  string
	ProfileDir string
}

type AttachOptions struct {
	BrowserID string
	Endpoint  string
}

type Driver interface {
	Launch(context.Context, LaunchOptions) (DriverSession, error)
	Attach(context.Context, AttachOptions) (DriverSession, error)
}

type DriverSession interface {
	AllowOrigins(context.Context, []string) error
	Navigate(context.Context, string, string) error
	Snapshot(context.Context, string) (RawSnapshot, error)
	InspectNode(context.Context, string, string) (NodeInspection, error)
	ListTabs(context.Context) ([]RawTab, error)
	OpenTab(context.Context, string) (RawTab, error)
	ActivateTab(context.Context, string) error
	CloseTab(context.Context, string) error
	Action(context.Context, DriverAction) (DriverActionResult, error)
	Capture(context.Context, CaptureRequest) (CaptureResult, error)
	Close() error
}

type NodeInspection struct {
	DocumentID string
	Node       RawNode
}

type RawTab struct {
	ID     string
	URL    string
	Title  string
	Active bool
}

type Tab struct {
	ID     string `json:"id"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
	Active bool   `json:"active"`
}

type RawSnapshot struct {
	TabID      string
	DocumentID string
	URL        string
	Title      string
	Nodes      []RawNode
	Truncated  bool
	Omitted    int
}

type RawNode struct {
	BackendNodeID string
	ParentID      string
	Depth         int
	Role          string
	Name          string
	Value         string
	Description   string
	InputType     string
	FormMethod    string
	FormAction    string
	TargetURL     string
	Disabled      bool
	Sensitive     bool
	Checked       *bool
	Selected      *bool
	Expanded      *bool
}

type Snapshot struct {
	BrowserID   string `json:"browser_id"`
	TabID       string `json:"tab_id"`
	Generation  uint64 `json:"generation"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	Nodes       []Node `json:"nodes"`
	Truncated   bool   `json:"truncated"`
	Omitted     int    `json:"omitted_nodes"`
	Untrusted   bool   `json:"untrusted"`
	ContentNote string `json:"content_note"`
}

type Node struct {
	Ref         string `json:"ref"`
	ParentRef   string `json:"parent_ref,omitempty"`
	Depth       int    `json:"depth"`
	Role        string `json:"role,omitempty"`
	Name        string `json:"name,omitempty"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	Checked     *bool  `json:"checked,omitempty"`
	Selected    *bool  `json:"selected,omitempty"`
	Expanded    *bool  `json:"expanded,omitempty"`
}

type ActionKind string

const (
	ActionClick         ActionKind = "click"
	ActionType          ActionKind = "type"
	ActionSelect        ActionKind = "select"
	ActionCheck         ActionKind = "check"
	ActionKey           ActionKind = "key"
	ActionScroll        ActionKind = "scroll"
	ActionHover         ActionKind = "hover"
	ActionUpload        ActionKind = "upload"
	ActionDialogAccept  ActionKind = "dialog_accept"
	ActionDialogDismiss ActionKind = "dialog_dismiss"
)

type Key string

const (
	KeyEnter      Key = "Enter"
	KeyEscape     Key = "Escape"
	KeyTab        Key = "Tab"
	KeyArrowUp    Key = "ArrowUp"
	KeyArrowDown  Key = "ArrowDown"
	KeyArrowLeft  Key = "ArrowLeft"
	KeyArrowRight Key = "ArrowRight"
	KeyPageUp     Key = "PageUp"
	KeyPageDown   Key = "PageDown"
	KeyHome       Key = "Home"
	KeyEnd        Key = "End"
	KeyBackspace  Key = "Backspace"
	KeyDelete     Key = "Delete"
)

type Effect string

const (
	EffectObserve                     Effect = "observe"
	EffectReversible                  Effect = "reversible"
	EffectExternalSubmit              Effect = "external_submit"
	EffectSensitiveTransmission       Effect = "sensitive_transmission"
	EffectAuthenticatedRepresentation Effect = "authenticated_representation"
	EffectUpload                      Effect = "upload"
	EffectExecutableDownload          Effect = "executable_download"
	EffectPermission                  Effect = "permission"
	EffectAccessChange                Effect = "access_change"
	EffectDestructive                 Effect = "destructive"
)

func (e Effect) valid() bool {
	switch e {
	case EffectObserve, EffectReversible, EffectExternalSubmit, EffectSensitiveTransmission,
		EffectAuthenticatedRepresentation, EffectUpload, EffectExecutableDownload,
		EffectPermission, EffectAccessChange, EffectDestructive:
		return true
	default:
		return false
	}
}

func (e Effect) RequiresApproval() bool {
	return e.valid() && e != EffectObserve && e != EffectReversible
}

type Action struct {
	Kind           ActionKind
	Ref            string
	Text           string
	Values         []string
	Checked        *bool
	Key            Key
	DeltaX         int
	DeltaY         int
	Files          []string
	DeclaredEffect Effect
}

type ActionPlan struct {
	BrowserID       string     `json:"browser_id"`
	TabID           string     `json:"tab_id"`
	Kind            ActionKind `json:"kind"`
	Ref             string     `json:"ref,omitempty"`
	Role            string     `json:"role,omitempty"`
	Name            string     `json:"name,omitempty"`
	DerivedEffect   Effect     `json:"derived_effect"`
	DeclaredEffect  Effect     `json:"declared_effect,omitempty"`
	EffectiveEffect Effect     `json:"effective_effect"`
	Attached        bool       `json:"attached"`
	TextBytes       int        `json:"text_bytes,omitempty"`
	FileCount       int        `json:"file_count,omitempty"`
}

type DriverAction struct {
	TabID         string
	BackendNodeID string
	Kind          ActionKind
	Text          string
	Values        []string
	Checked       *bool
	Key           Key
	DeltaX        int
	DeltaY        int
	Files         []string
}

type DriverActionResult struct {
	Navigated bool
	Download  *Download
	Dialog    *Dialog
}

type ActionResult struct {
	Plan      ActionPlan `json:"plan"`
	Navigated bool       `json:"navigated"`
	Download  *Download  `json:"download,omitempty"`
	Dialog    *Dialog    `json:"dialog,omitempty"`
	RefsStale bool       `json:"refs_stale"`
}

type Download struct {
	SuggestedName string `json:"suggested_name"`
	MediaType     string `json:"media_type,omitempty"`
	Bytes         []byte `json:"-"`
	Executable    bool   `json:"executable"`
}

type Dialog struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type CaptureRequest struct {
	TabID    string
	FullPage bool
}

type CaptureResult struct {
	MediaType string
	Bytes     []byte
	Width     int
	Height    int
}

type OpenRequest struct {
	Key            SessionKey
	Mode           Mode
	AttachEndpoint string
}

type OpenResult struct {
	BrowserID string `json:"browser_id"`
	Mode      Mode   `json:"mode"`
	Tabs      []Tab  `json:"tabs"`
}

type BeforeAction func(ActionPlan) error

func validateTabID(tabID string) error {
	if strings.TrimSpace(tabID) == "" || len(tabID) > 512 || strings.ContainsRune(tabID, '\x00') {
		return browserError(ErrorInvalidRequest, "tab id is invalid", "use a tab id returned by browser.tabs", false, nil)
	}
	return nil
}

func actionValidationError(format string, args ...any) error {
	return browserError(ErrorInvalidRequest, fmt.Sprintf(format, args...), "refresh the snapshot and submit a closed typed action", false, nil)
}
