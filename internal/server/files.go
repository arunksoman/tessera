package server

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// maxEntries bounds one folder listing; the picker then says the list was cut.
const maxEntries = 5000

type fileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// filesJSON describes one path for the file picker: a folder and what is in it, a file, or
// nothing yet, as for a database the user is about to create.
type filesJSON struct {
	Path         string      `json:"path"`
	Kind         string      `json:"kind"` // dir, file, or missing
	Parent       string      `json:"parent,omitempty"`
	ParentExists bool        `json:"parentExists,omitempty"`
	Size         int64       `json:"size,omitempty"`
	Entries      []fileEntry `json:"entries,omitempty"`
	Truncated    bool        `json:"truncated,omitempty"`
	// Denied holds why a folder could not be read.
	Denied string   `json:"denied,omitempty"`
	Home   string   `json:"home"`
	Roots  []string `json:"roots"`
	Sep    string   `json:"sep"`
}

// files lets the connection form pick a file on this machine. Like file fields themselves,
// it is open only on a loopback server, where the browser user owns the machine.
func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	if !s.api.allowFiles {
		writeError(w, http.StatusForbidden, "files", "only a local tess serve can browse files")
		return
	}
	home, _ := os.UserHomeDir()
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		p = home
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		fail(w, badf("%v", err))
		return
	}
	out := filesJSON{Path: abs, Home: home, Roots: roots(), Sep: string(filepath.Separator)}
	if parent := filepath.Dir(abs); parent != abs {
		out.Parent = parent
	}
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		out.Kind = "missing"
		if pi, err := os.Stat(out.Parent); err == nil && pi.IsDir() {
			out.ParentExists = true
		}
	case err != nil:
		fail(w, badf("%v", err))
		return
	case !info.IsDir():
		out.Kind, out.Size = "file", info.Size()
	default:
		out.Kind = "dir"
		out.Entries, out.Truncated, err = list(abs)
		if err != nil {
			out.Denied = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// list reads a folder, folders first, then by name ignoring case. A link counts as what it
// points to.
func list(dir string) ([]fileEntry, bool, error) {
	des, err := os.ReadDir(dir)
	out := make([]fileEntry, 0, min(len(des), maxEntries))
	for _, de := range des[:min(len(des), maxEntries)] {
		e := fileEntry{Name: de.Name(), Dir: de.IsDir()}
		if de.Type()&fs.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, e.Name)); err == nil {
				e.Dir = info.IsDir()
			}
		}
		if !e.Dir {
			if info, err := de.Info(); err == nil {
				e.Size = info.Size()
			}
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b fileEntry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, len(des) > maxEntries, err
}

// roots are the drives on Windows and / elsewhere.
func roots() []string {
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	var out []string
	for c := 'C'; c <= 'Z'; c++ { // A: and B: are floppy drives, which can stall a check
		d := string(c) + `:\`
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}
