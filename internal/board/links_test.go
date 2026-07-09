package board

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"no links", "just plain text, nothing bracketed", nil},
		{"single bracket is not a link", "not a link: [foo] here", nil},
		{"single link", "see [[design-notes]] for details", []string{"design-notes"}},
		{
			"multiple links",
			"compare [[foo]] against [[bar]] and [[baz]]",
			[]string{"foo", "bar", "baz"},
		},
		{
			"punctuation adjacency",
			"([[foo]]). Also, [[bar]]! and [[baz]],[[qux]].",
			[]string{"foo", "bar", "baz", "qux"},
		},
		{
			"adjacent links no separator",
			"[[a]][[b]]",
			[]string{"a", "b"},
		},
		{
			"link with dot and space in name",
			"read [[design.notes]] then [[open questions]]",
			[]string{"design.notes", "open questions"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractLinks(c.text)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ExtractLinks(%q) = %#v, want %#v", c.text, got, c.want)
			}
		})
	}
}

func TestResolveLink(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", "/tmp/session-notes-test-boards")
	cases := []struct {
		name    string
		session string
		link    string
		want    string
	}{
		{"simple name", "abc-123", "design-notes", "/tmp/session-notes-test-boards/abc-123.notes/design-notes.md"},
		{"name with dot", "sess-1", "design.notes", "/tmp/session-notes-test-boards/sess-1.notes/design.notes.md"},
		{"name with spaces", "sess-2", "open questions", "/tmp/session-notes-test-boards/sess-2.notes/open questions.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &Board{Frontmatter: Frontmatter{Session: c.session}}
			got := ResolveLink(b, c.link)
			want := filepath.FromSlash(c.want)
			if got != want {
				t.Errorf("ResolveLink(%q) = %q, want %q", c.link, got, want)
			}
		})
	}
}

func TestResolveLinkPaths(t *testing.T) {
	t.Setenv("HOME", filepath.FromSlash("/home/tester"))
	cwd := filepath.FromSlash("/work/repo")
	cases := []struct {
		name string
		link string
		want string
	}{
		{"nested slash name", "docs/foo.md", "/work/repo/docs/foo.md"},
		{"dot relative", "./notes/plan.md", "/work/repo/notes/plan.md"},
		{"dot-dot relative", "../sibling/x.md", "/work/sibling/x.md"},
		{"absolute", "/etc/hosts", "/etc/hosts"},
		{"tilde home", "~/todo.md", "/home/tester/todo.md"},
		{"extension preserved", "src/main.go", "/work/repo/src/main.go"},
		{"no extension kept as-is", "bin/tool", "/work/repo/bin/tool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &Board{Frontmatter: Frontmatter{Session: "sess", Cwd: cwd}}
			got := ResolveLink(b, c.link)
			want := filepath.FromSlash(c.want)
			if got != want {
				t.Errorf("ResolveLink(%q) = %q, want %q", c.link, got, want)
			}
		})
	}
}

func TestIsPathLink(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"plain", false},
		{"open questions", false},
		{"design.notes", false},
		{"docs/foo.md", true},
		{"./x", true},
		{"../y", true},
		{"~/z", true},
		{"~", true},
		{"/abs/path", true},
	}
	for _, c := range cases {
		if got := IsPathLink(c.name); got != c.want {
			t.Errorf("IsPathLink(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
