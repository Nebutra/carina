package rpc

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const gatewayTokenPrefix = "gw1"

// GatewayTokenClaims is the scoped, signed capability envelope used by future
// Gateway transports. It is not a shared-secret owner password: role, scopes,
// transport binding, and expiry are part of the signed payload.
type GatewayTokenClaims struct {
	Version   string   `json:"v"`
	Subject   string   `json:"sub,omitempty"`
	Role      Role     `json:"role"`
	Scopes    []Scope  `json:"scopes"`
	Transport string   `json:"transport,omitempty"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	Notes     []string `json:"notes,omitempty"`
}

type GatewayTokenIssuer struct {
	secret []byte
	now    func() time.Time
}

func NewGatewayTokenIssuer(secret []byte) (*GatewayTokenIssuer, error) {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("gateway token secret: %w", err)
		}
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("gateway token secret must be at least 32 bytes")
	}
	return &GatewayTokenIssuer{secret: append([]byte(nil), secret...), now: time.Now}, nil
}

func (i *GatewayTokenIssuer) Issue(subject string, role Role, scopes []Scope, ttl time.Duration, transport string) (string, GatewayTokenClaims, error) {
	if ttl <= 0 {
		return "", GatewayTokenClaims{}, fmt.Errorf("ttl_seconds must be > 0")
	}
	if len(scopes) == 0 {
		return "", GatewayTokenClaims{}, fmt.Errorf("gateway token scopes are required")
	}
	role, negotiated, notes, err := NegotiateScopes(role, scopes)
	if err != nil {
		return "", GatewayTokenClaims{}, err
	}
	if len(negotiated) == 0 {
		return "", GatewayTokenClaims{}, fmt.Errorf("no requested gateway token scopes are authorized for role %q", role)
	}
	now := i.now().UTC()
	claims := GatewayTokenClaims{
		Version:   "1",
		Subject:   strings.TrimSpace(subject),
		Role:      role,
		Scopes:    negotiated,
		Transport: strings.TrimSpace(transport),
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
		Notes:     notes,
	}
	token, err := i.sign(claims)
	if err != nil {
		return "", GatewayTokenClaims{}, err
	}
	return token, claims, nil
}

func (i *GatewayTokenIssuer) Verify(token, transport string) (GatewayTokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != gatewayTokenPrefix {
		return GatewayTokenClaims{}, fmt.Errorf("invalid gateway token format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return GatewayTokenClaims{}, fmt.Errorf("invalid gateway token payload")
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return GatewayTokenClaims{}, fmt.Errorf("invalid gateway token signature")
	}
	mac := hmac.New(sha256.New, i.secret)
	_, _ = mac.Write([]byte(parts[1]))
	if !hmac.Equal(gotSig, mac.Sum(nil)) {
		return GatewayTokenClaims{}, fmt.Errorf("invalid gateway token signature")
	}
	var claims GatewayTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return GatewayTokenClaims{}, fmt.Errorf("invalid gateway token claims: %w", err)
	}
	if claims.Version != "1" {
		return GatewayTokenClaims{}, fmt.Errorf("unsupported gateway token version %q", claims.Version)
	}
	role, canonical, _, err := NegotiateScopes(claims.Role, claims.Scopes)
	if err != nil {
		return GatewayTokenClaims{}, err
	}
	if role != claims.Role || len(canonical) == 0 || !sameScopes(canonical, claims.Scopes) {
		return GatewayTokenClaims{}, fmt.Errorf("gateway token claims are not canonical for role %q", claims.Role)
	}
	if claims.ExpiresAt <= i.now().UTC().Unix() {
		return GatewayTokenClaims{}, fmt.Errorf("gateway token expired")
	}
	if want := strings.TrimSpace(transport); want != "" && claims.Transport != want {
		return GatewayTokenClaims{}, fmt.Errorf("gateway token transport mismatch")
	}
	return claims, nil
}

// IntersectScopes returns requested scopes narrowed to a signed token's
// available scopes. An empty request means "use the token exactly".
func IntersectScopes(available, requested []Scope) ([]Scope, error) {
	if len(available) == 0 {
		return nil, fmt.Errorf("gateway token has no scopes")
	}
	if len(requested) == 0 {
		return append([]Scope(nil), available...), nil
	}
	allowed := make(map[Scope]bool, len(available))
	for _, scope := range available {
		if !ValidScope(scope) {
			return nil, fmt.Errorf("unsupported gateway scope %q", scope)
		}
		allowed[scope] = true
	}
	out := make([]Scope, 0, len(requested))
	seen := map[Scope]bool{}
	for _, scope := range requested {
		if !ValidScope(scope) {
			return nil, fmt.Errorf("unsupported gateway scope %q", scope)
		}
		if allowed[scope] && !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	sortScopes(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("no requested gateway scopes are authorized")
	}
	return out, nil
}

func sameScopes(a, b []Scope) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (i *GatewayTokenIssuer) sign(claims GatewayTokenClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, i.secret)
	_, _ = mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return gatewayTokenPrefix + "." + encoded + "." + sig, nil
}
