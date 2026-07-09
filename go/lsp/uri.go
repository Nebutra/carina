// file:// URI construction and parsing (docs/plans/code-intelligence.md, V3
// D3). Real language servers percent-encode the URIs they emit and expect
// encoded URIs in requests: PathToURI percent-encodes path segments
// (RFC 3986, '/' preserved), URIToPath percent-decodes and rejects any
// non-file scheme.
package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
)

// PathToURI renders an absolute filesystem path as a file:// URI with each
// path segment percent-encoded ('/' separators preserved).
func PathToURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// URIToPath parses a file:// URI back into a percent-decoded filesystem
// path. ok is false for any other scheme (the caller must drop the location
// rather than treat the URI text as a path).
func URIToPath(uri string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, "file://")
	if !ok {
		return "", false
	}
	if u, err := url.Parse(uri); err == nil && u.Path != "" {
		return u.Path, true
	}
	// Unparseable percent escapes from a sloppy server: best-effort raw path
	// (still a file: scheme, so never dropped for encoding alone).
	return rest, true
}

// sameFileURI reports whether two file:// URIs name the same file, matching
// through percent-decoding and symlink-canonical path comparison (V4 D2):
// servers answer with encoded, canonicalized URIs (/private/tmp) for files
// the client opened via a symlinked or raw-concatenated URI (/tmp). Either
// URI failing to decode falls back to the exact string compare.
func sameFileURI(a, b string) bool {
	if a == b {
		return true
	}
	pa, oka := URIToPath(a)
	pb, okb := URIToPath(b)
	if !oka || !okb {
		return false
	}
	return pa == pb || canonicalURIPath(pa) == canonicalURIPath(pb)
}

// canonicalURIPath resolves symlinks for URI equality checks. When the leaf
// does not exist the parent directory canonicalizes instead; a fully
// unresolvable path stays cleaned — it can only ever fail a compare, never
// widen one.
func canonicalURIPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	if r, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(r, base)
	}
	return p
}
