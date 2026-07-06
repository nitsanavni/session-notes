package tui

import (
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"
)

// watcher wraps fsnotify. It watches the board file's directory (not the file
// itself) because atomic writes replace the inode via rename; directory
// watching keeps working across replacements.
type watcher struct {
	fw   *fsnotify.Watcher
	path string // absolute board path we care about
	ch   chan struct{}
	done chan struct{}
}

func newWatcher(path string) (*watcher, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(filepath.Dir(abs)); err != nil {
		fw.Close()
		return nil, err
	}
	w := &watcher{fw: fw, path: abs, ch: make(chan struct{}, 1), done: make(chan struct{})}
	go w.loop()
	return w, nil
}

func (w *watcher) loop() {
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			if ev.Name != w.path {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			select {
			case w.ch <- struct{}{}:
			default: // coalesce bursts
			}
		case _, ok := <-w.fw.Errors:
			if !ok {
				return
			}
		}
	}
}

// wait returns a command that delivers a reloadMsg on the next file change.
func (w *watcher) wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-w.ch:
			return reloadMsg{}
		case <-w.done:
			return nil
		}
	}
}

func (w *watcher) close() {
	close(w.done)
	w.fw.Close()
}
