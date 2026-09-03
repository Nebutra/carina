//go:build !darwin && !linux

package browser

import "fmt"

func execChromeProcess(string, []string, []string) error {
	return fmt.Errorf("native browser launcher is unsupported on this platform")
}
