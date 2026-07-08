package rpc

import (
	"strings"
	"testing"
	"time"
)

func TestGatewayTokenIssuer(t *testing.T) {
	issuer, err := NewGatewayTokenIssuer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1000, 0).UTC()
	issuer.now = func() time.Time { return base }
	token, claims, err := issuer.Issue("local-owner", RoleOperator, []Scope{ScopeRead, ScopeAdmin, ScopeWorker}, time.Minute, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Role != RoleOperator || len(claims.Scopes) != 2 || claims.Scopes[0] != ScopeRead || claims.Scopes[1] != ScopeAdmin {
		t.Fatalf("unexpected negotiated claims: %+v", claims)
	}
	verified, err := issuer.Verify(token, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if verified.Subject != "local-owner" || verified.Transport != "ws" {
		t.Fatalf("verified claims mismatch: %+v", verified)
	}
	if _, err := issuer.Verify(token, "http"); err == nil {
		t.Fatal("transport mismatch should fail")
	}
	if _, err := issuer.Verify(token+"x", "ws"); err == nil {
		t.Fatal("tampered token should fail")
	}
	issuer.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := issuer.Verify(token, "ws"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired token should fail, got %v", err)
	}
}

func TestGatewayTokenValidation(t *testing.T) {
	if _, err := NewGatewayTokenIssuer([]byte("short")); err == nil {
		t.Fatal("short token secret should fail")
	}
	issuer, err := NewGatewayTokenIssuer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := issuer.Issue("", Role("root"), nil, time.Minute, ""); err == nil {
		t.Fatal("unknown role should fail")
	}
	if _, _, err := issuer.Issue("", RoleObserver, nil, 0, ""); err == nil {
		t.Fatal("non-positive ttl should fail")
	}
	if _, _, err := issuer.Issue("", RoleObserver, nil, time.Minute, ""); err == nil {
		t.Fatal("empty token scopes should fail")
	}
	if _, _, err := issuer.Issue("", RoleObserver, []Scope{ScopeAdmin}, time.Minute, ""); err == nil {
		t.Fatal("role-disallowed token scopes should fail")
	}
	if _, _, err := issuer.IssueWithRoutes("", RoleOperator, []Scope{ScopeRead}, []string{"v1/models"}, time.Minute, "http"); err == nil {
		t.Fatal("route without leading slash should fail")
	}
}

func TestGatewayTokenRejectsNonCanonicalClaims(t *testing.T) {
	issuer, err := NewGatewayTokenIssuer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1000, 0).UTC()
	issuer.now = func() time.Time { return base }
	for name, claims := range map[string]GatewayTokenClaims{
		"observer-admin": {
			Version:   "1",
			Role:      RoleObserver,
			Scopes:    []Scope{ScopeAdmin},
			IssuedAt:  base.Unix(),
			ExpiresAt: base.Add(time.Minute).Unix(),
		},
		"duplicate": {
			Version:   "1",
			Role:      RoleOperator,
			Scopes:    []Scope{ScopeRead, ScopeRead},
			IssuedAt:  base.Unix(),
			ExpiresAt: base.Add(time.Minute).Unix(),
		},
		"unsorted": {
			Version:   "1",
			Role:      RoleOperator,
			Scopes:    []Scope{ScopeAdmin, ScopeRead},
			IssuedAt:  base.Unix(),
			ExpiresAt: base.Add(time.Minute).Unix(),
		},
		"unbound-transport": {
			Version:   "1",
			Role:      RoleOperator,
			Scopes:    []Scope{ScopeRead},
			IssuedAt:  base.Unix(),
			ExpiresAt: base.Add(time.Minute).Unix(),
		},
		"unsorted-routes": {
			Version:   "1",
			Role:      RoleOperator,
			Scopes:    []Scope{ScopeRead},
			Routes:    []string{"/v1/models", "/plugins/*", "/tools/invoke"},
			IssuedAt:  base.Unix(),
			ExpiresAt: base.Add(time.Minute).Unix(),
			Transport: "ws",
		},
	} {
		token, err := issuer.sign(claims)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := issuer.Verify(token, "ws"); err == nil {
			t.Fatalf("%s token should fail verification", name)
		}
	}
}

func TestIntersectScopes(t *testing.T) {
	got, err := IntersectScopes([]Scope{ScopeRead, ScopeAdmin}, []Scope{ScopeAdmin, ScopeRead, ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != ScopeRead || got[1] != ScopeAdmin {
		t.Fatalf("unexpected intersected scopes: %+v", got)
	}
	if _, err := IntersectScopes([]Scope{ScopeRead}, []Scope{ScopeAdmin}); err == nil {
		t.Fatal("unauthorized requested scope should fail")
	}
}

func TestGatewayTokenRoutes(t *testing.T) {
	issuer, err := NewGatewayTokenIssuer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	token, claims, err := issuer.IssueWithRoutes("http-client", RoleOperator, []Scope{ScopeRead, ScopeWrite}, []string{"/tools/invoke", "/v1/*", "/tools/invoke"}, time.Minute, "http")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.Routes) != 2 || claims.Routes[0] != "/tools/invoke" || claims.Routes[1] != "/v1/*" {
		t.Fatalf("routes were not canonicalized: %+v", claims.Routes)
	}
	verified, err := issuer.Verify(token, "http")
	if err != nil {
		t.Fatal(err)
	}
	if !RouteAllowed(verified.Routes, "/v1/models") {
		t.Fatalf("/v1/models should be allowed by wildcard route: %+v", verified.Routes)
	}
	if RouteAllowed(verified.Routes, "/plugins/x") {
		t.Fatalf("/plugins/x should not be allowed: %+v", verified.Routes)
	}
}
