package daemon

// IdentityProvider resolves a caller's identity and roles for role-based
// approval (PRD §5 Phase 5: SSO integration interface). This is the seam an
// enterprise plugs an SSO/OIDC backend into; the default LocalIdentity
// treats the OS user as an admin so local single-user use is unaffected.
type IdentityProvider interface {
	// Resolve maps an opaque token (e.g. an OIDC access token) to a user id
	// and their roles.
	Resolve(token string) (userID string, roles []string, err error)
}

// LocalIdentity is the default: a single local admin. Enterprises replace it
// with an SSO-backed implementation.
type LocalIdentity struct {
	User string
}

func (l LocalIdentity) Resolve(_ string) (string, []string, error) {
	user := l.User
	if user == "" {
		user = "local"
	}
	return user, []string{"admin"}, nil
}
