package egress

import "net/http"

// CredentialResolver returns the secret value for a name, or ok=false if it is
// absent. The proxy resolves credentials at the network boundary so the agent's
// command children never see them (carina-run's env allowlist already excludes
// the secret from the child environment).
type CredentialResolver func(secretName string) (value string, ok bool)

// InjectionRule authenticates outbound requests to a host by setting a header
// from a resolved secret: header value = ValuePrefix + <secret>.
type InjectionRule struct {
	Header      string // e.g. "Authorization" (defaults to Authorization if empty)
	ValuePrefix string // e.g. "Bearer "
	SecretName  string // key passed to the resolver
	// MITM opts this host into TLS interception so the credential can be injected
	// on HTTPS (not just plain HTTP). Higher-risk: requires the child to trust the
	// egress CA. Off by default; plain-HTTP injection needs no interception.
	MITM bool
}

// Injector applies per-host credential injection. Injection is opt-in per host:
// only hosts with a rule are authenticated, and only if their secret resolves.
type Injector struct {
	rules   map[string]InjectionRule // host -> rule
	resolve CredentialResolver
}

// NewInjector builds an injector from host rules and a resolver.
func NewInjector(rules map[string]InjectionRule, resolve CredentialResolver) *Injector {
	return &Injector{rules: rules, resolve: resolve}
}

// injects reports whether a host has an injection rule.
func (in *Injector) injects(host string) bool {
	if in == nil {
		return false
	}
	_, ok := in.rules[host]
	return ok
}

// mitm reports whether a host is opted into TLS interception for HTTPS injection.
func (in *Injector) mitm(host string) bool {
	if in == nil {
		return false
	}
	r, ok := in.rules[host]
	return ok && r.MITM
}

// hasMITM reports whether any host requires TLS interception.
func (in *Injector) hasMITM() bool {
	if in == nil {
		return false
	}
	for _, r := range in.rules {
		if r.MITM {
			return true
		}
	}
	return false
}

// apply sets the credential header for host if a rule exists and its secret
// resolves; returns whether a header was injected. The secret value is never
// logged. An already-present header is overwritten so the injected credential
// always wins for the configured host.
func (in *Injector) apply(host string, h http.Header) bool {
	if in == nil {
		return false
	}
	rule, ok := in.rules[host]
	if !ok {
		return false
	}
	val, ok := in.resolve(rule.SecretName)
	if !ok || val == "" {
		return false // secret absent/denied; forward unauthenticated rather than fail
	}
	header := rule.Header
	if header == "" {
		header = "Authorization"
	}
	h.Set(header, rule.ValuePrefix+val)
	return true
}

// Hosts returns the hosts with injection rules, so the caller can union them
// into the egress allowlist (an injected host must also be reachable).
func (in *Injector) Hosts() []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in.rules))
	for h := range in.rules {
		out = append(out, h)
	}
	return out
}
