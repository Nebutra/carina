package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

type harnessBootstrapResult struct {
	WorkspaceRoot string    `json:"workspace_root"`
	GatewayURL    string    `json:"gateway_url"`
	Token         string    `json:"token"`
	Role          string    `json:"role"`
	ExpiresAt     time.Time `json:"expires_at"`
}

var (
	resolveHarnessRuntime = localruntime.Resolve
	connectHarnessRuntime = localdaemon.ConnectOrStart
)

// cmdHarness exposes the deliberately narrow browser bootstrap contract. It
// uses the owner Unix socket and never prints an admin token or runtime
// diagnostics on failure.
func cmdHarness(args []string) error {
	if len(args) == 0 || args[0] != "bootstrap" {
		return fmt.Errorf("usage: carina harness bootstrap --workspace PATH --role observer|operator --origin ORIGIN --json")
	}
	fs := flag.NewFlagSet("carina harness bootstrap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace path")
	role := fs.String("role", "", "observer or operator")
	origin := fs.String("origin", "", "browser origin")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || !*jsonOutput || strings.TrimSpace(*workspace) == "" || strings.TrimSpace(*origin) == "" {
		return fmt.Errorf("usage: carina harness bootstrap --workspace PATH --role observer|operator --origin ORIGIN --json")
	}
	roleValue := strings.ToLower(strings.TrimSpace(*role))
	var scopes []rpc.Scope
	switch roleValue {
	case "observer":
		scopes = []rpc.Scope{rpc.ScopeRead, rpc.ScopeStream}
	case "operator":
		scopes = []rpc.Scope{rpc.ScopeRead, rpc.ScopeWrite, rpc.ScopeStream}
	default:
		return fmt.Errorf("unsupported harness role %q", *role)
	}
	originValue, err := rpc.NormalizeGatewayOrigin(*origin)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	resolution, err := resolveHarnessRuntime(home, *workspace, localruntime.ModeWorkspace)
	if err != nil {
		return err
	}
	client, _, err := connectHarnessRuntime(resolution.Spec)
	if err != nil {
		return err
	}
	defer client.Close()
	var ensured struct {
		GatewayURL string `json:"gateway_url"`
	}
	if err := client.Call("gateway.local.ensure", map[string]any{"origin": originValue}, &ensured); err != nil {
		return err
	}
	if strings.TrimSpace(ensured.GatewayURL) == "" {
		return fmt.Errorf("gateway.local.ensure returned an empty gateway_url")
	}
	var issued struct {
		Token  string `json:"token"`
		Claims struct {
			ExpiresAt int64 `json:"exp"`
		} `json:"claims"`
	}
	if err := client.Call("gateway.token.issue", map[string]any{
		"subject":     "carina-harness-desktop",
		"role":        roleValue,
		"scopes":      scopes,
		"ttl_seconds": 300,
		"transport":   "ws",
		"tenant_id":   "local",
	}, &issued); err != nil {
		return err
	}
	if strings.TrimSpace(issued.Token) == "" || issued.Claims.ExpiresAt <= 0 {
		return fmt.Errorf("gateway.token.issue returned incomplete credentials")
	}
	return printJSON(harnessBootstrapResult{
		WorkspaceRoot: resolution.Workspace.CanonicalRoot,
		GatewayURL:    ensured.GatewayURL,
		Token:         issued.Token,
		Role:          roleValue,
		ExpiresAt:     time.Unix(issued.Claims.ExpiresAt, 0).UTC(),
	})
}
