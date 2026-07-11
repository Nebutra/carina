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
	Rollback          func() error
}

func Plan(findings []Finding, home string) []Action {
	var out []Action
	for _, f := range findings {
		dir := filepath.Join(home, ".carina", "state")
		if f.Name == "state_dir" && f.State == "FAIL" {
			created := false
			out = append(out, Action{Name: "state_dir", Description: "create " + dir + " with mode 0700", Apply: func() error {
				if _, err := os.Stat(dir); os.IsNotExist(err) {
					created = true
				}
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return err
				}
				return os.Chmod(dir, 0o700)
			}, Rollback: func() error {
				if created {
					return os.Remove(dir)
				}
				return nil
			}})
		}
		if f.Name == "state_dir_permissions" && (f.State == "WARN" || f.State == "FAIL") {
			var prior os.FileMode
			out = append(out, Action{Name: "state_dir_permissions", Description: "restrict " + dir + " to mode 0700", Apply: func() error {
				info, err := os.Stat(dir)
				if err != nil {
					return err
				}
				prior = info.Mode().Perm()
				return os.Chmod(dir, 0o700)
			}, Rollback: func() error {
				if prior == 0 {
					return nil
				}
				return os.Chmod(dir, prior)
			}})
		}
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
	applied := make([]Action, 0, len(actions))
	for _, a := range actions {
		if err := a.Apply(); err != nil {
			for i := len(applied) - 1; i >= 0; i-- {
				if applied[i].Rollback != nil {
					_ = applied[i].Rollback()
				}
			}
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		applied = append(applied, a)
	}
	return nil
}
