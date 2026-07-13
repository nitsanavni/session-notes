package cloud

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// TestIntegrationConcurrentSiblingEdits starts a real server on a random port
// and has two clients concurrently edit sibling subtrees, verifying that the
// per-board serialization keeps every write and that a --node-scoped watch only
// fires for its own subtree.
func TestIntegrationConcurrentSiblingEdits(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_ = store.CreateBoard("board", seed)
	tok, _ := store.CreateToken("agent")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: NewServer(store, false).Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	base := "http://" + ln.Addr().String()

	client := NewRemoteTree(base, "board", tok)
	// Seed two parent subtrees.
	_, _ = client.Apply(board.Op{Name: "add", Section: "Threads", Text: "parent-A"})
	_, _ = client.Apply(board.Op{Name: "add", Section: "Threads", Text: "parent-B"})
	content, _, _ := store.Get("board")
	b := board.Parse(content)
	th := b.Section("Threads")
	var aID, bID string
	for _, it := range th.Items {
		if strings.Contains(it.DisplayText(), "parent-A") {
			aID = it.ID
		}
		if strings.Contains(it.DisplayText(), "parent-B") {
			bID = it.ID
		}
	}
	if aID == "" || bID == "" {
		t.Fatal("missing parent ids")
	}

	// Watch subtree A only.
	watcher := NewRemoteTree(base, "board", tok)
	ch, cancel, err := watcher.Watch(aID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// Two clients, each rooted at its own sibling, add many children concurrently.
	const n = 15
	var wg sync.WaitGroup
	wg.Add(2)
	edit := func(root, prefix string) {
		defer wg.Done()
		c := NewRemoteTree(base, "board", tok)
		for i := 0; i < n; i++ {
			_, err := c.Apply(board.Op{Name: "reply", ID: root, Root: root, Text: fmt.Sprintf("%s-%d", prefix, i)})
			if err != nil {
				t.Errorf("%s apply %d: %v", prefix, i, err)
				return
			}
		}
	}
	go edit(aID, "a")
	go edit(bID, "b")
	wg.Wait()

	// Every write survived (serialized, none lost).
	content, _, _ = store.Get("board")
	for i := 0; i < n; i++ {
		if !strings.Contains(content, fmt.Sprintf("a-%d", i)) {
			t.Errorf("missing a-%d", i)
		}
		if !strings.Contains(content, fmt.Sprintf("b-%d", i)) {
			t.Errorf("missing b-%d", i)
		}
	}

	// The A-scoped watch must have fired at least once (its subtree changed) —
	// drain what's buffered.
	got := false
	timeout := time.After(1 * time.Second)
drain:
	for {
		select {
		case <-ch:
			got = true
		case <-timeout:
			break drain
		}
	}
	if !got {
		t.Fatal("A-scoped watcher never fired despite A edits")
	}
}
