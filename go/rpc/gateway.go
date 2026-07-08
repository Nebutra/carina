package rpc

import (
	"fmt"
	"sort"
	"strings"
)

const GatewayProtocolVersion = 1

// Role is the coarse Gateway client role. It is negotiated in hello responses
// and is intentionally separate from per-method scopes.
type Role string

const (
	RoleObserver Role = "observer"
	RoleOperator Role = "operator"
	RoleWorker   Role = "worker"
	RoleNode     Role = "node"
)

// HelloRequest is the transport-neutral Gateway handshake request. It is not
// an auth token exchange; it lets clients discover the protocol contract they
// would use over JSON-RPC, future WS, or future HTTP surfaces.
type HelloRequest struct {
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	ClientID        string   `json:"client_id,omitempty"`
	Role            Role     `json:"role,omitempty"`
	Scopes          []Scope  `json:"scopes,omitempty"`
	Token           string   `json:"token,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	UserAgent       string   `json:"user_agent,omitempty"`
}

// HelloResponse is the server's negotiated Gateway contract snapshot.
type HelloResponse struct {
	Version         string             `json:"version"`
	ProtocolVersion int                `json:"protocol_version"`
	ServerVersion   string             `json:"server_version"`
	ClientID        string             `json:"client_id,omitempty"`
	RequestedRole   Role               `json:"requested_role,omitempty"`
	Role            Role               `json:"role"`
	Scopes          []Scope            `json:"scopes"`
	Features        []string           `json:"features"`
	Methods         []MethodDescriptor `json:"methods"`
	Auth            map[string]any     `json:"auth"`
	Notes           []string           `json:"notes,omitempty"`
}

func BuildHelloResponse(req HelloRequest, serverVersion string, methods []MethodDescriptor) (HelloResponse, error) {
	if req.ProtocolVersion != 0 && req.ProtocolVersion != GatewayProtocolVersion {
		return HelloResponse{}, fmt.Errorf("unsupported gateway protocol version %d", req.ProtocolVersion)
	}
	role, scopes, notes, err := NegotiateScopes(req.Role, req.Scopes)
	if err != nil {
		return HelloResponse{}, err
	}
	return HelloResponse{
		Version:         "1",
		ProtocolVersion: GatewayProtocolVersion,
		ServerVersion:   serverVersion,
		ClientID:        strings.TrimSpace(req.ClientID),
		RequestedRole:   req.Role,
		Role:            role,
		Scopes:          scopes,
		Features: []string{
			"json_rpc",
			"method_catalog",
			"dynamic_scopes",
			"transport_origin_policy",
		},
		Methods: methods,
		Auth: map[string]any{
			"grant_type": "none",
			"enforced_by": []string{
				"transport_origin",
				"method_descriptor",
				"capability_kernel",
			},
		},
		Notes: notes,
	}, nil
}

// NegotiateScopes returns the role and scopes for a Gateway handshake. This is
// a protocol contract, not a permission grant; handlers and the kernel still
// enforce actual authority.
func NegotiateScopes(requestedRole Role, requested []Scope) (Role, []Scope, []string, error) {
	role := requestedRole
	if role == "" {
		role = RoleObserver
	}
	max, ok := roleScopes(role)
	if !ok {
		return "", nil, nil, fmt.Errorf("unsupported gateway role %q", requestedRole)
	}
	if len(requested) == 0 {
		return role, max, []string{"empty scope request defaulted to role maximum"}, nil
	}
	allowed := make(map[Scope]bool, len(max))
	for _, scope := range max {
		allowed[scope] = true
	}
	out := make([]Scope, 0, len(requested))
	seen := map[Scope]bool{}
	for _, scope := range requested {
		if !ValidScope(scope) {
			return "", nil, nil, fmt.Errorf("unsupported gateway scope %q", scope)
		}
		if !allowed[scope] || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	sortScopes(out)
	return role, out, nil, nil
}

func roleScopes(role Role) ([]Scope, bool) {
	switch role {
	case RoleObserver:
		return []Scope{ScopeRead, ScopeStream}, true
	case RoleOperator:
		return []Scope{ScopeRead, ScopeWrite, ScopeAdmin, ScopeStream}, true
	case RoleWorker:
		return []Scope{ScopeRead, ScopeWorker, ScopeStream}, true
	case RoleNode:
		return []Scope{ScopeRead, ScopeWorker, ScopeStream}, true
	default:
		return nil, false
	}
}

func sortScopes(scopes []Scope) {
	order := map[Scope]int{
		ScopeRead:   0,
		ScopeWrite:  1,
		ScopeAdmin:  2,
		ScopeWorker: 3,
		ScopeStream: 4,
	}
	sort.Slice(scopes, func(i, j int) bool {
		oi, okI := order[scopes[i]]
		oj, okJ := order[scopes[j]]
		if okI && okJ {
			return oi < oj
		}
		return scopes[i] < scopes[j]
	})
}
