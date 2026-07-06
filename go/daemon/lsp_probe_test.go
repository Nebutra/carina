package daemon

import "testing"

func TestServerForExtMatrix(t *testing.T) {
	cases := map[string]string{
		".go":  "gopls",
		".ts":  "typescript-language-server",
		".py":  "pyright-langserver",
		".rs":  "rust-analyzer",
		".c":   "clangd",
		".cpp": "clangd",
		".zig": "zls",
		".rb":  "solargraph",
	}
	for ext, bin := range cases {
		srv, ok := serverForExt(ext)
		if !ok || srv.bin != bin {
			t.Errorf("%s: want %s, got %q ok=%v", ext, bin, srv.bin, ok)
		}
	}
	if _, ok := serverForExt(".unknownext"); ok {
		t.Error("an unknown extension must have no server")
	}
}

func TestHardenProcessNoError(t *testing.T) {
	// No-op on darwin; sets non-dumpable on Linux. Either way it must not error
	// on the platform running the test.
	if err := hardenProcess(); err != nil {
		t.Fatalf("hardenProcess returned an error: %v", err)
	}
}
