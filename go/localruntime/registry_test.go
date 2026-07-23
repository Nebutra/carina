package localruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanRegistryIsReadOnlyWhenMissing(t *testing.T) {
	home := t.TempDir()
	entries, err := ScanRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := os.Stat(RuntimeRoot(home)); !os.IsNotExist(err) {
		t.Fatalf("registry scan created root: %v", err)
	}
}

func TestScanRegistryReturnsStoppedDescriptor(t *testing.T) {
	home := t.TempDir()
	workspace := testWorkspace(t)
	spec, err := EnsureSpec(home, workspace, SpecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := DescriptorFromSpec(spec)
	stoppedAt := time.Now().UTC()
	descriptor.StoppedAt = &stoppedAt
	if err := WriteDescriptor(spec.Paths.DescriptorPath, descriptor); err != nil {
		t.Fatal(err)
	}
	entries, err := ScanRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Descriptor == nil || entries[0].Descriptor.Lifecycle != LifecycleStopped {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestScanRegistryReportsCorruptDescriptor(t *testing.T) {
	home := t.TempDir()
	workspace := testWorkspace(t)
	spec, err := EnsureSpec(home, workspace, SpecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.Paths.DescriptorPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ScanRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Spec == nil || !strings.Contains(entries[0].Error, "decode") {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestRegistryIgnoresUnrelatedFiles(t *testing.T) {
	home := t.TempDir()
	root := RuntimeRoot(home)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("not a runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ScanRegistry(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}
