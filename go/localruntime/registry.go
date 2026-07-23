package localruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type RegistryEntry struct {
	RuntimeDir string      `json:"runtime_dir"`
	Spec       *Spec       `json:"spec,omitempty"`
	Descriptor *Descriptor `json:"descriptor,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// ScanRegistry reads known workspace runtime metadata without creating
// directories, dialing sockets, or starting processes.
func ScanRegistry(home string) ([]RegistryEntry, error) {
	root := RuntimeRoot(home)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localruntime: scan registry: %w", err)
	}
	out := make([]RegistryEntry, 0, len(entries))
	for _, item := range entries {
		if !item.IsDir() {
			continue
		}
		runtimeDir := filepath.Join(root, item.Name())
		paths := Paths{
			RuntimeDir: runtimeDir, SpecPath: filepath.Join(runtimeDir, "spec.json"),
			DescriptorPath: filepath.Join(runtimeDir, "descriptor.json"),
		}
		entry := RegistryEntry{RuntimeDir: runtimeDir}
		spec, specErr := LoadSpec(paths.SpecPath)
		if specErr == nil {
			entry.Spec = &spec
		} else if !errors.Is(specErr, os.ErrNotExist) {
			entry.Error = specErr.Error()
		}
		descriptor, descriptorErr := LoadDescriptor(paths.DescriptorPath)
		if descriptorErr == nil {
			entry.Descriptor = &descriptor
		} else if !errors.Is(descriptorErr, os.ErrNotExist) {
			if entry.Error != "" {
				entry.Error += "; "
			}
			entry.Error += descriptorErr.Error()
		}
		if entry.Spec == nil && entry.Descriptor == nil && entry.Error == "" {
			continue
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := registrySortKey(out[i]), registrySortKey(out[j])
		if left == right {
			return out[i].RuntimeDir < out[j].RuntimeDir
		}
		return left < right
	})
	return out, nil
}

func registrySortKey(entry RegistryEntry) string {
	if entry.Descriptor != nil {
		return entry.Descriptor.Workspace.CanonicalRoot
	}
	if entry.Spec != nil {
		return entry.Spec.Workspace.CanonicalRoot
	}
	return entry.RuntimeDir
}
