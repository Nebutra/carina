package daemon

import "testing"

func TestGrokAuthDirectoryTooBroadAcrossPlatformRoots(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		goos  string
		broad bool
	}{
		{name: "Unix root", path: "/", goos: "linux", broad: true},
		{name: "Unix owner directory", path: "/home/tester/.grok", goos: "linux", broad: false},
		{name: "drive root", path: `C:\`, goos: "windows", broad: true},
		{name: "forward-slash drive root", path: `C:/`, goos: "windows", broad: true},
		{name: "drive owner directory", path: `C:\Users\tester\.grok`, goos: "windows", broad: false},
		{name: "drive-relative directory", path: `C:.grok`, goos: "windows", broad: true},
		{name: "UNC share root", path: `\\server\share\`, goos: "windows", broad: true},
		{name: "UNC owner directory", path: `\\server\share\tester\.grok`, goos: "windows", broad: false},
		{name: "extended drive root", path: `\\?\C:\`, goos: "windows", broad: true},
		{name: "extended drive owner directory", path: `\\?\C:\Users\tester\.grok`, goos: "windows", broad: false},
		{name: "extended UNC root", path: `\\?\UNC\server\share\`, goos: "windows", broad: true},
		{name: "extended UNC owner directory", path: `\\?\UNC\server\share\tester\.grok`, goos: "windows", broad: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := grokAuthDirectoryTooBroadForOS(test.path, test.goos); got != test.broad {
				t.Fatalf("tooBroad(%q, %q)=%v, want %v", test.path, test.goos, got, test.broad)
			}
		})
	}
}
