package config

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type duplicateJSONKeyError struct {
	objectPath string
	key        string
}

func (e *duplicateJSONKeyError) Error() string {
	return fmt.Sprintf("duplicate JSON key %q in object %s; remove one of the duplicate entries", e.key, e.objectPath)
}

// rejectDuplicateJSONKeys performs a syntax-aware scan before decoding into a
// map or struct, where encoding/json would otherwise silently keep the last
// occurrence of an object key.
func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := scanJSONValue(dec, "$", 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func scanJSONValue(dec *json.Decoder, path string, depth int) error {
	if depth > 10_000 {
		return fmt.Errorf("JSON nesting exceeds 10000 levels")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key at %s is not a string", path)
			}
			if _, exists := seen[key]; exists {
				return &duplicateJSONKeyError{objectPath: path, key: key}
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(dec, jsonObjectPath(path, key), depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("expected JSON object terminator at %s", path)
		}
	case '[':
		index := 0
		for dec.More() {
			if err := scanJSONValue(dec, fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
				return err
			}
			index++
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("expected JSON array terminator at %s", path)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delim, path)
	}
	return nil
}

func jsonObjectPath(parent, key string) string {
	data, _ := json.Marshal(key)
	return parent + "[" + string(data) + "]"
}
