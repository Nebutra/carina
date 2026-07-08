package daemon

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
)

type nebutraMemoryIdentity struct {
	UserID         string
	OrganizationID string
	ClaimsVersion  string
	Source         string
	Authenticated  bool
}

func resolveNebutraMemoryIdentity() nebutraMemoryIdentity {
	if identity := identityFromJSONEnv("CARINA_NEBUTRA_IDENTITY_JSON"); identity.UserID != "" {
		identity.Source = "CARINA_NEBUTRA_IDENTITY_JSON"
		identity.Authenticated = true
		return identity
	}
	if identity := identityFromTokenClaims(os.Getenv("CARINA_NEBUTRA_TOKEN")); identity.UserID != "" {
		identity.Source = "CARINA_NEBUTRA_TOKEN:claims"
		identity.Authenticated = true
		return identity
	}
	if identity := identityFromUserEnv(); identity.UserID != "" {
		identity.Source = "CARINA_NEBUTRA_USER_ID"
		return identity
	}
	return nebutraMemoryIdentity{Source: "local"}
}

func identityFromJSONEnv(name string) nebutraMemoryIdentity {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nebutraMemoryIdentity{}
	}
	var claims map[string]any
	if err := json.Unmarshal([]byte(raw), &claims); err != nil {
		return nebutraMemoryIdentity{}
	}
	return identityFromClaims(claims)
}

func identityFromTokenClaims(token string) nebutraMemoryIdentity {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return nebutraMemoryIdentity{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nebutraMemoryIdentity{}
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nebutraMemoryIdentity{}
	}
	return identityFromClaims(claims)
}

func identityFromUserEnv() nebutraMemoryIdentity {
	userID := identityClaim(map[string]any{
		"user_id": os.Getenv("CARINA_NEBUTRA_USER_ID"),
	}, "user_id")
	if userID == "" {
		return nebutraMemoryIdentity{}
	}
	orgID := identityClaim(map[string]any{
		"organization_id": os.Getenv("CARINA_NEBUTRA_ORGANIZATION_ID"),
		"org_id":          os.Getenv("CARINA_NEBUTRA_ORG_ID"),
	}, "organization_id", "org_id")
	return nebutraMemoryIdentity{
		UserID:         userID,
		OrganizationID: orgID,
		ClaimsVersion:  "v1",
	}
}

func identityFromClaims(claims map[string]any) nebutraMemoryIdentity {
	userID := identityClaim(claims, "userId", "user_id", "sub")
	if userID == "" {
		return nebutraMemoryIdentity{}
	}
	claimsVersion := identityClaim(claims, "claimsVersion", "claims_version")
	if claimsVersion == "" {
		claimsVersion = "v1"
	}
	return nebutraMemoryIdentity{
		UserID:         userID,
		OrganizationID: identityClaim(claims, "organizationId", "organization_id", "nebutra:organization_id", "tenantId", "tenant_id"),
		ClaimsVersion:  claimsVersion,
	}
}

func identityClaim(claims map[string]any, names ...string) string {
	for _, name := range names {
		raw, ok := claims[name]
		if !ok {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 1024 {
			continue
		}
		return value
	}
	return ""
}

func memoryProfileKey(identity nebutraMemoryIdentity) string {
	if identity.UserID == "" {
		return "local"
	}
	user := shortIdentityHash(identity.UserID)
	if identity.OrganizationID != "" {
		return "nebutra_org_" + shortIdentityHash(identity.OrganizationID) + "_user_" + user
	}
	return "nebutra_user_" + user
}

func shortIdentityHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:8])
}
