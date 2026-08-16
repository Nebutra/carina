package daemon

import (
	"bytes"
	"fmt"
)

// materializeEdit replaces exactly one occurrence of old in current.
// Empty, missing, or non-unique old is a hard error; callers must not propose
// a patch after this fails.
func materializeEdit(old, new string, current []byte) ([]byte, error) {
	if old == "" {
		return nil, fmt.Errorf("edit old must be a non-empty exact span")
	}
	needle := []byte(old)
	switch bytes.Count(current, needle) {
	case 0:
		return nil, fmt.Errorf("edit old not found")
	case 1:
		return bytes.Replace(current, needle, []byte(new), 1), nil
	default:
		return nil, fmt.Errorf("edit old is not unique")
	}
}
