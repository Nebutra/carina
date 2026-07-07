package rpc

import (
	"fmt"
	"sort"
	"strings"
)

// Scope describes the least authority a method needs inside Carina's control
// plane. Today it is enforced for transport exposure and discovery; future
// Gateway handshakes can bind these scopes to authenticated clients directly.
type Scope string

const (
	ScopeRead   Scope = "read"
	ScopeWrite  Scope = "write"
	ScopeAdmin  Scope = "admin"
	ScopeWorker Scope = "worker"
	ScopeStream Scope = "stream"
)

// MethodDescriptor is the machine-readable control-plane policy for one RPC
// method. It mirrors the Gateway descriptor idea: registration, discovery, and
// remote exposure must agree on the same record.
type MethodDescriptor struct {
	Method            string `json:"method"`
	Scope             Scope  `json:"scope"`
	Remote            bool   `json:"remote"`
	Stream            bool   `json:"stream,omitempty"`
	Advertise         bool   `json:"advertise"`
	DynamicScope      bool   `json:"dynamic_scope,omitempty"`
	ControlPlaneWrite bool   `json:"control_plane_write,omitempty"`
}

// ValidScope reports whether scope is part of the Gateway control-plane model.
func ValidScope(scope Scope) bool {
	switch scope {
	case ScopeRead, ScopeWrite, ScopeAdmin, ScopeWorker, ScopeStream:
		return true
	default:
		return false
	}
}

func (d MethodDescriptor) normalized(stream bool) (MethodDescriptor, error) {
	d.Method = strings.TrimSpace(d.Method)
	if d.Method == "" {
		return d, fmt.Errorf("rpc descriptor method is required")
	}
	if d.Scope == "" {
		return d, fmt.Errorf("rpc descriptor %q missing scope", d.Method)
	}
	if !ValidScope(d.Scope) {
		return d, fmt.Errorf("rpc descriptor %q has invalid scope %q", d.Method, d.Scope)
	}
	d.Stream = stream
	if !d.Advertise {
		// Phase A publishes every classified method. Hidden classified methods
		// should use an explicit tri-state later instead of relying on bool zero
		// values.
		d.Advertise = true
	}
	return d, nil
}

// MethodDescriptors returns a deterministic copy for discovery surfaces.
func (s *Server) MethodDescriptors() []MethodDescriptor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MethodDescriptor, 0, len(s.descriptors))
	for _, d := range s.descriptors {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out
}
