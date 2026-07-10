package daemon

import (
	"os/exec"
	"reflect"
	"strings"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/provider"
)

// This file holds the pure, side-effect-free parts of P1.6's `carina
// doctor` probe logic — the pieces that need no live kernel/session so they
// are directly unit-testable, separate from handleDoctor's own
// kernel-reachability round trip (already covered by doctor_test.go).

// doctorDisabledEnv is the exact env var name for the kill-switch, following
// the CARINA_<NOUN>_<VERB> naming convention already used by
// CARINA_INDEX_SWEEP, CARINA_PROVIDER_REFRESH, CARINA_REASONER_MODEL.
const doctorDisabledEnv = "CARINA_DOCTOR_DISABLE"

// doctorDisabled reports whether the CARINA_DOCTOR_DISABLE kill-switch is
// set, accepting the same truthy spellings as CARINA_INDEX_SWEEP
// (off|0|false all count as explicitly unset; anything else non-empty
// counts as "on" per the plan's "accept multiple truthy spellings"
// precedent — the kill-switch's "on" values are 1/true/on, mirroring the
// existing strings.ToLower/TrimSpace pattern in codeintel.go's
// sweepEnabled).
func doctorDisabled(getenv func(string) string) bool {
	v := strings.ToLower(strings.TrimSpace(getenv(doctorDisabledEnv)))
	switch v {
	case "", "0", "false", "off":
		return false
	default:
		return true
	}
}

// providerKeyStatus is the per-provider BYOK resolution status doctor
// reports: which providers have a resolvable key (store or env) vs none.
type providerKeyStatus struct {
	ProviderID string
	Resolved   bool
	Source     string // "store", "env:VARNAME", or "" when unresolved
}

// byokProbe reports, per provider in the catalog, whether some credential
// resolves — store first, then each Env var name in order — matching BYOK's
// "every user pays for their own tokens" contract: doctor's job is to
// confirm SOME key exists, not that one specific provider's key exists.
// hasStoredCred and getenv are injected so this is testable without a real
// go/auth.Store or real process environment.
func byokProbe(providers []struct {
	ID  string
	Env []string
}, hasStoredCred func(providerID string) bool, getenv func(string) string) []providerKeyStatus {
	out := make([]providerKeyStatus, 0, len(providers))
	for _, p := range providers {
		st := providerKeyStatus{ProviderID: p.ID}
		switch {
		case hasStoredCred(p.ID):
			st.Resolved = true
			st.Source = "store"
		default:
			for _, envVar := range p.Env {
				if getenv(envVar) != "" {
					st.Resolved = true
					st.Source = "env:" + envVar
					break
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// anyProviderResolved is the pass/fail rollup for the BYOK probe: pass if
// at least one provider resolves a key.
func anyProviderResolved(statuses []providerKeyStatus) bool {
	for _, s := range statuses {
		if s.Resolved {
			return true
		}
	}
	return false
}

// byokProviderList narrows a provider.Catalog to the {ID, Env} shape
// byokProbe needs, sorted by ID for deterministic doctor output.
func byokProviderList(cat provider.Catalog) []struct {
	ID  string
	Env []string
} {
	out := make([]struct {
		ID  string
		Env []string
	}, 0, len(cat))
	for _, info := range provider.Sorted(cat) {
		out = append(out, struct {
			ID  string
			Env []string
		}{ID: info.ID, Env: info.Env})
	}
	return out
}

// lspServerStatus is the per-language-server presence status doctor
// reports: langID/binary from serverForExt's matrix, plus whether the
// binary resolves on PATH and a copy-paste remediation hint when it does
// not.
type lspServerStatus struct {
	LangID      string
	Bin         string
	Present     bool
	Remediation string // "" when Present; install hint otherwise
}

// lspLangIDs is the fixed, deterministic probe order (serverForExt's
// switch has no natural iteration order, so this pins one for doctor
// output).
var lspLangIDs = []string{"go", "typescript", "python", "rust", "cpp", "zig", "ruby"}

// lspExtForLang is the representative extension lspProbe feeds into
// serverForExt per langID (serverForExt is keyed by extension, doctor wants
// one row per language).
var lspExtForLang = map[string]string{
	"go": ".go", "typescript": ".ts", "python": ".py", "rust": ".rs",
	"cpp": ".c", "zig": ".zig", "ruby": ".rb",
}

// lspRemediation maps a language server binary to a copy-paste install
// command. Best-effort: covers the common package managers for each
// server; anything unlisted falls back to a generic "install <bin>" hint.
var lspRemediation = map[string]string{
	"gopls":                      "go install golang.org/x/tools/gopls@latest",
	"typescript-language-server": "npm install -g typescript-language-server typescript",
	"pyright-langserver":         "npm install -g pyright",
	"rust-analyzer":              "rustup component add rust-analyzer",
	"clangd":                     "install clangd via your OS package manager (e.g. apt install clangd, brew install llvm)",
	"zls":                        "install zls from https://github.com/zigtools/zls",
	"solargraph":                 "gem install solargraph",
}

// lspProbe reports presence of each language server in serverForExt's
// matrix. lookPath is injected (exec.LookPath in production) so this is
// testable without depending on the real PATH.
func lspProbe(lookPath func(string) (string, error)) []lspServerStatus {
	out := make([]lspServerStatus, 0, len(lspLangIDs))
	for _, langID := range lspLangIDs {
		srv, ok := serverForExt(lspExtForLang[langID])
		if !ok {
			continue
		}
		st := lspServerStatus{LangID: langID, Bin: srv.bin}
		if _, err := lookPath(srv.bin); err == nil {
			st.Present = true
		} else {
			st.Remediation = lspRemediation[srv.bin]
			if st.Remediation == "" {
				st.Remediation = "install " + srv.bin
			}
		}
		out = append(out, st)
	}
	return out
}

// realLookPath is lspProbe's production lookPath: os/exec.LookPath.
func realLookPath(bin string) (string, error) { return exec.LookPath(bin) }

// policyBundleStale reports whether the enterprise policy bundle currently
// on disk under dir has diverged from loaded — the *kernel.OrgPolicy
// snapshot the running daemon actually initialized its kernel sessions
// with at startup (New()'s one-time loadOrgPolicy(opts.PolicyDir) call).
//
// This closes a real governance gap: ApplyConfig (reload.go) explicitly
// documents that a config/SIGHUP reload does NOT re-init kernel/policy
// wiring (restart-only), so an operator editing bundle.toml/trusted-keys/
// approval.json on disk and expecting `carina doctor` to confirm the
// change landed would otherwise see a false PASS — doctor's prior kernel
// probe only checked liveness (ClassifyCommand("echo ok")), never whether
// the loaded policy generation matches disk.
//
// dir == "" (no PolicyDir configured) always reports fresh — nothing to go
// stale for a deployment that never opted into an on-disk policy bundle.
func policyBundleStale(dir string, loaded *kernel.OrgPolicy) (stale bool, reason string) {
	if dir == "" {
		return false, ""
	}
	onDisk := loadOrgPolicy(dir)
	if reflect.DeepEqual(onDisk, loaded) {
		return false, ""
	}
	return true, "policy bundle at " + dir + " has changed on disk since the daemon last loaded it — restart carina-daemon to apply the change (config reload does not re-init policy wiring)"
}
