// Package doctorfix plans and applies narrowly-scoped, reversible diagnostics repairs.
package doctorfix

import (
	"fmt"
	"os"
	"path/filepath"
)

type Finding struct{ Name, State, Detail string }
type Action struct {
	Name, Description string
	Apply             func() error
}

func Plan(findings []Finding, home string) []Action {
	var out []Action
	for _, f := range findings {
		if f.Name != "state_dir" || f.State != "FAIL" {
			continue
		}
		dir := filepath.Join(home, ".carina", "state")
		out = append(out, Action{Name: "state_dir", Description: "create " + dir + " with mode 0700", Apply: func() error {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			return os.Chmod(dir, 0o700)
		}})
	}
	return out
}

func Apply(actions []Action, confirmed bool) error {
	if len(actions) == 0 {
		return nil
	}
	if !confirmed {
		return fmt.Errorf("repairs require confirmation (--yes)")
	}
	for _, a := range actions {
		if err := a.Apply(); err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
	}
	return nil
}
