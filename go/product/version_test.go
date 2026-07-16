package product

import (
	"regexp"
	"testing"
)

func TestVersionIsReleaseSemver(t *testing.T) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(Version) {
		t.Fatalf("product version %q is not release semver", Version)
	}
	if Version != "0.6.3" {
		t.Fatalf("next release version = %s, want 0.6.3", Version)
	}
}
