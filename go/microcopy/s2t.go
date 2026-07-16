package microcopy

// ToTraditional converts authored Simplified Chinese product copy to
// Traditional Chinese. Exact catalog strings use a generated OpenCC-compatible
// phrase table; unknown strings fall back to per-rune conversion so dynamic
// fragments still convert.
//
// Source of truth remains the zh (Simplified) catalogs. Regenerate the tables
// with: python3 scripts/gen_zh_hant.py
func ToTraditional(s string) string {
	if s == "" {
		return s
	}
	if t, ok := s2tExact[s]; ok {
		return t
	}
	runes := []rune(s)
	changed := false
	for i, r := range runes {
		if t, ok := s2tChar[r]; ok {
			runes[i] = t
			changed = true
		}
	}
	if !changed {
		return s
	}
	return string(runes)
}
