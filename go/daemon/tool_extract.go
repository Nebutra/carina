package daemon

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nebutra/carina/go/toolchain"
)

const (
	maxSearchExtractFiles = 20
	maxSearchWhyBytes     = 120
	maxListRootFiles      = 12
	maxListDirs           = 16
	maxListDirExamples    = 2
)

func formatSearchObservation(pattern string, matches []toolchain.Match, workspaceRoot string) string {
	if len(matches) == 0 {
		return "no matches"
	}
	type group struct {
		file string
		n    int
		line int
		why  string
	}
	var groups []group
	index := map[string]int{}
	for _, m := range matches {
		file := displayRelPath(m.File, workspaceRoot)
		if file == "" {
			continue
		}
		if i, ok := index[file]; ok {
			groups[i].n++
			continue
		}
		index[file] = len(groups)
		groups = append(groups, group{
			file: file,
			n:    1,
			line: m.Line,
			why:  oneLineWhy(m.Text, maxSearchWhyBytes),
		})
	}
	if len(groups) == 0 {
		return "no matches"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "search %q: %d matches in %d files\n", pattern, len(matches), len(groups))
	shown := 0
	for i, g := range groups {
		if i >= maxSearchExtractFiles {
			break
		}
		fmt.Fprintf(&b, "- %s (%d): L%d %s\n", g.file, g.n, g.line, g.why)
		shown++
	}
	if omitted := len(groups) - shown; omitted > 0 {
		fmt.Fprintf(&b, "… %d files omitted; read a path or narrow the pattern\n", omitted)
	}
	return b.String()
}

func formatListObservation(files []toolchain.FileEntry, truncated bool, workspaceRoot string) string {
	type dirGroup struct {
		name     string
		count    int
		examples []string
	}
	var root []toolchain.FileEntry
	dirs := map[string]*dirGroup{}
	var dirOrder []string
	for _, f := range files {
		rel := displayRelPath(f.Path, workspaceRoot)
		if rel == "" || rel == "." {
			continue
		}
		head, _, ok := strings.Cut(rel, "/")
		if !ok {
			root = append(root, toolchain.FileEntry{Path: rel, Size: f.Size, Language: f.Language})
			continue
		}
		g := dirs[head]
		if g == nil {
			g = &dirGroup{name: head}
			dirs[head] = g
			dirOrder = append(dirOrder, head)
		}
		g.count++
		if len(g.examples) < maxListDirExamples {
			g.examples = append(g.examples, rel)
		}
	}
	sort.Slice(root, func(i, j int) bool { return root[i].Path < root[j].Path })
	sort.SliceStable(dirOrder, func(i, j int) bool {
		left, right := dirs[dirOrder[i]], dirs[dirOrder[j]]
		if left.count != right.count {
			return left.count > right.count
		}
		return left.name < right.name
	})

	var b strings.Builder
	fmt.Fprintf(&b, "list: %d files", len(files))
	if len(dirs) > 0 {
		fmt.Fprintf(&b, " in %d dirs", len(dirs))
	}
	if truncated {
		fmt.Fprintf(&b, " (truncated at %d files, depth %d)", listFileCap, listDepthCap)
	}
	b.WriteByte('\n')

	shownRoot := 0
	for i, f := range root {
		if i >= maxListRootFiles {
			break
		}
		b.WriteString("- ")
		b.WriteString(formatListFileWhy(f))
		b.WriteByte('\n')
		shownRoot++
	}
	shownDirs := 0
	for i, name := range dirOrder {
		if i >= maxListDirs {
			break
		}
		g := dirs[name]
		fmt.Fprintf(&b, "- %s/ (%d)", g.name, g.count)
		if len(g.examples) > 0 {
			b.WriteString(": ")
			b.WriteString(strings.Join(g.examples, ", "))
		}
		b.WriteByte('\n')
		shownDirs++
	}
	omitted := (len(root) - shownRoot) + (len(dirOrder) - shownDirs)
	if truncated || omitted > 0 {
		if omitted > 0 {
			fmt.Fprintf(&b, "… %d more; use search or code.map for the rest\n", omitted)
		} else {
			fmt.Fprintf(&b, "… use search or code.map for the rest\n")
		}
	}
	return b.String()
}

func formatListFileWhy(f toolchain.FileEntry) string {
	if f.Language != "" {
		return fmt.Sprintf("%s (%d B, %s)", f.Path, f.Size, f.Language)
	}
	return fmt.Sprintf("%s (%d B)", f.Path, f.Size)
}

func displayRelPath(path, root string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) && strings.TrimSpace(root) != "" {
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			path = rel
		}
	}
	return strings.TrimPrefix(filepath.ToSlash(path), "./")
}

func oneLineWhy(s string, maxBytes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxBytes <= 0 {
		return s
	}
	return truncateUTF8Bytes(s, maxBytes)
}
