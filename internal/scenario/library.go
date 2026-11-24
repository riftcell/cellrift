package scenario

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LibraryEntry holds metadata about a scenario in the built-in library.
type LibraryEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Protocols   []string `json:"protocols"`
	Path        string   `json:"path"`
}

// ScanLibrary recursively scans dirs for .yaml scenario files and returns
// their metadata. Only valid, parseable files are included.
func ScanLibrary(dirs ...string) []LibraryEntry {
	var entries []LibraryEntry

	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !isScenarioFile(path) {
				return nil
			}
			if s, err := ParseFile(path); err == nil {
				entries = append(entries, libraryEntry(path, s))
			}
			return nil
		})
	}

	return sortedByPath(entries)
}

// ScanLibraryFS is ScanLibrary over an fs.FS — the embedded copy of the
// library the binary carries for `cellrift list` outside a clone. Paths in
// the returned entries are relative to the FS root (e.g. "ims/basic_call.yaml").
func ScanLibraryFS(fsys fs.FS) []LibraryEntry {
	var entries []LibraryEntry

	fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isScenarioFile(path) {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil
		}
		if s, err := Parse(data, path); err == nil {
			entries = append(entries, libraryEntry(path, s))
		}
		return nil
	})

	return sortedByPath(entries)
}

func isScenarioFile(path string) bool {
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}

// libraryEntry summarises one parsed scenario for a listing: sorted protocol
// names and a description truncated for table display.
func libraryEntry(path string, s *Scenario) LibraryEntry {
	protocols := make([]string, 0, len(s.Protocols))
	for p := range s.Protocols {
		protocols = append(protocols, p)
	}
	sort.Strings(protocols)

	desc := strings.TrimSpace(s.Description)
	if len(desc) > 80 {
		desc = desc[:77] + "..."
	}

	return LibraryEntry{Name: s.Name, Description: desc, Protocols: protocols, Path: path}
}

func sortedByPath(entries []LibraryEntry) []LibraryEntry {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries
}
