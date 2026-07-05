package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// decodeStrictJSON unmarshals JSON but first rejects duplicate object keys.
// encoding/json silently keeps the LAST of duplicate keys, which lets a crafted
// spec/config hide a value behind a second key (e.g. two "profile" keys, one
// benign for a human reader, one that actually takes effect). For
// security-relevant inputs (workflow specs, policy/config) we fail closed.
func decodeStrictJSON(data []byte, v any) error {
	if err := checkNoDupKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// checkNoDupKeys walks the token stream and rejects any object with a repeated
// key at any nesting depth.
func checkNoDupKeys(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil // scalar
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := kt.(string)
			if !ok {
				return fmt.Errorf("strict json: non-string object key")
			}
			if seen[key] {
				return fmt.Errorf("strict json: duplicate key %q", key)
			}
			seen[key] = true
			if err := checkNoDupKeys(dec); err != nil { // the value
				return err
			}
		}
		_, err := dec.Token() // consume '}'
		return err
	case '[':
		for dec.More() {
			if err := checkNoDupKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume ']'
		return err
	}
	return nil
}
