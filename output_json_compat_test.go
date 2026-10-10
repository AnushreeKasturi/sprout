package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The streaming writer must produce what the previous encoding/json-based
// implementation produced. legacyWriteJSON below is that implementation,
// kept verbatim as the reference.

type legacyNode struct{ *Node }

func (n legacyNode) MarshalJSON() ([]byte, error) {
	typ := "file"
	if n.IsDir {
		typ = "directory"
	}
	var errMsg, modified string
	if !n.ModTime.IsZero() {
		modified = n.ModTime.UTC().Format(time.RFC3339)
	}
	if n.Err != nil {
		errMsg = errReason(n.Err)
	}
	kids := make([]legacyNode, len(n.Children))
	for i, c := range n.Children {
		kids[i] = legacyNode{c}
	}
	if len(kids) == 0 {
		kids = nil
	}
	return json.Marshal(struct {
		Name      string       `json:"name"`
		Path      string       `json:"path,omitempty"`
		Type      string       `json:"type"`
		Size      int64        `json:"size,omitempty"`
		Modified  string       `json:"modified,omitempty"`
		Error     string       `json:"error,omitempty"`
		Truncated bool         `json:"truncated,omitempty"`
		Status    string       `json:"status,omitempty"`
		Changes   int          `json:"changes,omitempty"`
		Added     int          `json:"added,omitempty"`
		Deleted   int          `json:"deleted,omitempty"`
		Churn     int          `json:"churn,omitempty"`
		MoreFiles int          `json:"moreFiles,omitempty"`
		Children  []legacyNode `json:"children,omitempty"`
	}{n.Name, n.Rel, typ, n.Size, modified, errMsg, n.Truncated, n.Status, n.Changes, n.Added, n.Deleted, n.Churn, n.More, kids})
}

func legacyWriteJSON(t *testing.T, tree *Tree, path string, pretty bool) []byte {
	t.Helper()
	s := &Stats{Languages: map[string]int{}}
	collectStats(tree.Root, s)
	abs, _ := filepath.Abs(path)
	projects := DetectProject(path)
	if projects == nil {
		projects = []ProjectInfo{}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	if pretty {
		enc.SetIndent("", "  ")
	}
	err := enc.Encode(struct {
		SchemaVersion int           `json:"schemaVersion"`
		Name          string        `json:"name"`
		Root          string        `json:"root"`
		GitAware      bool          `json:"gitAware"`
		Projects      []ProjectInfo `json:"projects"`
		Summary       struct {
			Directories int            `json:"directories"`
			Files       int            `json:"files"`
			Bytes       int64          `json:"bytes"`
			Skipped     int            `json:"skipped"`
			Languages   map[string]int `json:"languages"`
		} `json:"summary"`
		Tree legacyNode `json:"tree"`
	}{
		SchemaVersion: jsonSchemaVersion,
		Name:          filepath.Base(abs),
		Root:          filepath.ToSlash(abs),
		GitAware:      tree.GitAware,
		Projects:      projects,
		Summary: struct {
			Directories int            `json:"directories"`
			Files       int            `json:"files"`
			Bytes       int64          `json:"bytes"`
			Skipped     int            `json:"skipped"`
			Languages   map[string]int `json:"languages"`
		}{s.Directories, s.Files, s.TotalSize, tree.Skipped, s.Languages},
		Tree: legacyNode{tree.Root},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// awkward strings: escapes, HTML-sensitive characters, control bytes,
// invalid UTF-8, line/paragraph separators, non-ASCII, emoji.
var awkward = []string{
	"", "plain.go", `quo"te`, `back\slash`, "tab\there", "nl\nx", "cr\rx", "bs\bx", "ff\fx",
	"\x00\x01\x1f\x7f", "<script>&</script>", "café 日本 \U0001F331",
	"bad\xffutf8\xc3", " sep ", "a\u007fb", "\xed\xa0\x80surrogate",
}

func richTree() *Tree {
	mod := time.Date(2026, 9, 26, 12, 30, 45, 123, time.FixedZone("X", 5*3600))
	root := &Node{Name: "root", IsDir: true}
	for i, s := range awkward {
		root.Children = append(root.Children, &Node{Name: s, Rel: "d/" + s, Size: int64(i * 1000), ModTime: mod})
	}
	sub := &Node{
		Name: "sub", Rel: "sub", IsDir: true, Size: 99, ModTime: mod, Truncated: true, Status: "M",
		Changes: 3, Added: 10, Deleted: 2, Churn: 7, More: 4,
		Err:      &fs.PathError{Op: "open", Path: "sub", Err: fs.ErrPermission},
		Children: []*Node{{Name: "x<y>&z", Rel: "sub/x", Status: "?", Churn: 1}},
	}
	root.Children = append(root.Children, sub,
		&Node{Name: "empty-dir", Rel: "empty-dir", IsDir: true, Children: []*Node{}},
		&Node{Name: "errored", Rel: "errored", IsDir: true, Err: errors.New("boom \"quoted\"")},
		&Node{Name: "deleted.go", Rel: "deleted.go", Status: "D", Deleted: 40},
	)
	return &Tree{Root: root, Skipped: 12, GitAware: true}
}

func decode(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b)
	}
	return v
}

func TestStreamingJSONMatchesLegacy(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n")
	write(t, dir, "package.json", "{}\n")
	tree := richTree()
	for _, pretty := range []bool{false, true} {
		var got bytes.Buffer
		if err := WriteJSON(&got, tree, dir, pretty); err != nil {
			t.Fatal(err)
		}
		want := legacyWriteJSON(t, tree, dir, pretty)
		if !reflect.DeepEqual(decode(t, got.Bytes()), decode(t, want)) {
			t.Errorf("pretty=%v: decoded values differ\n got: %s\nwant: %s", pretty, got.Bytes(), want)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("pretty=%v: bytes differ\n got: %s\nwant: %s", pretty, got.Bytes(), want)
		}
	}
}

func TestStreamingJSONMatchesLegacyOnRealTrees(t *testing.T) {
	// This repository, with sizes and times, and a git-status tree.
	for _, args := range [][]string{{"--json"}, {"--json", "--git"}, {"--json", "--churn"}, {"--json", "--size", "-L", "1"}} {
		for _, pretty := range []bool{false, true} {
			a := append([]string{".", "--no-config"}, args...)
			if pretty {
				a = append(a, "--pretty")
			}
			out, errOut, code := runCLI(t, a...)
			if code != 0 {
				t.Fatalf("%v: exit %d: %s", a, code, errOut)
			}
			f := parseFlagsForTest(t, a)
			tree, _, err := load(".", f, contains(args, "--git"), "", io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if contains(args, "--churn") {
				if err := addChurn(".", tree, ""); err != nil {
					t.Fatal(err)
				}
			}
			want := legacyWriteJSON(t, tree, ".", pretty)
			if !reflect.DeepEqual(decode(t, []byte(out)), decode(t, want)) {
				t.Errorf("%v: decoded values differ from the legacy encoder", a)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// parseFlagsForTest builds the same Options run() would, for a JSON run.
func parseFlagsForTest(t *testing.T, args []string) Options {
	t.Helper()
	f, path, err := parseFlags(args, &bytes.Buffer{})
	if err != nil || path != "." {
		t.Fatalf("parseFlags(%v): %v %q", args, err, path)
	}
	return Options{
		ShowHidden: f.hidden || f.all || f.ai || f.entry,
		MaxDepth:   f.depth,
		NoIgnore:   f.noIgnore || f.all,
		Sizes:      f.size || f.sortBy != "name",
		Stat:       f.json || f.stats || f.ai || f.entry,
	}
}

func TestAppendJSONStringMatchesEncodingJSON(t *testing.T) {
	for _, s := range awkward {
		checkString(t, s)
	}
	// every single byte, alone and inside text
	for b := 0; b < 256; b++ {
		checkString(t, string([]byte{byte(b)}))
		checkString(t, "a"+string([]byte{byte(b)})+"z")
	}
}

func checkString(t *testing.T, s string) {
	t.Helper()
	want, _ := json.Marshal(s)
	if got := appendJSONString(nil, s); !bytes.Equal(got, want) {
		t.Errorf("appendJSONString(%q) = %s, want %s", s, got, want)
	}
}

func FuzzAppendJSONString(f *testing.F) {
	for _, s := range awkward {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want, _ := json.Marshal(s)
		if got := appendJSONString(nil, s); !bytes.Equal(got, want) {
			t.Fatalf("appendJSONString(%q) = %s, want %s", s, got, want)
		}
	})
}

func TestNodeMarshalJSONStillWorks(t *testing.T) {
	tree := richTree()
	got, err := json.Marshal(tree.Root)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(legacyNode{tree.Root})
	if !bytes.Equal(got, want) {
		t.Errorf("json.Marshal(node) changed:\n got: %s\nwant: %s", got, want)
	}
}
