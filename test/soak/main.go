// Command soak is an overnight load/soak harness for the session-notes cloud
// server (`session-notes server`). It drives a fresh SQLite-backed server under
// sustained multi-agent load and continuously checks a set of correctness and
// resource invariants, then exercises graceful shutdown + crash-recovery.
//
// It is a standalone test utility: it talks to the server over HTTP only, and
// imports internal/board solely for the offline parse/id-uniqueness invariant.
// It has no tests, so `go test ./...` merely builds it; it does not affect the
// suite. Run it manually:
//
//	go build -o /tmp/sn . && \
//	go run ./test/soak -bin /tmp/sn -db /tmp/soak/server.db -dur 50m
//
// The harness never touches ports or state outside the paths/addr it is given.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nitsanavni/session-notes/internal/board"
)

// ---- config ----

type config struct {
	bin        string
	addr       string
	db         string
	reportPath string
	logPath    string
	numBoards  int
	numAgents  int
	dur        time.Duration
	checkpoint time.Duration
	throttleMS int // per-agent-step sleep floor (bounds op rate + disk/RSS growth)
	rssCapMB   int // abort load if server RSS exceeds this (host safety)
	durCap     int // per-board cap on tracked "durable" adds (rest are churned)
	ephCap     int // per-scope cap on ephemeral (deletable) items
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.bin, "bin", "", "path to a prebuilt session-notes binary (built from repo if empty)")
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:9700", "server listen address")
	flag.StringVar(&cfg.db, "db", "", "SQLite db path (required; lives in the soak dir)")
	flag.StringVar(&cfg.reportPath, "report", "", "path to write the findings report (default <dbdir>/report.md)")
	flag.IntVar(&cfg.numBoards, "boards", 5, "number of boards to create")
	flag.IntVar(&cfg.numAgents, "agents", 20, "number of concurrent worker agents")
	flag.DurationVar(&cfg.dur, "dur", 50*time.Minute, "soak duration")
	flag.DurationVar(&cfg.checkpoint, "checkpoint", 5*time.Minute, "invariant-checkpoint interval")
	flag.IntVar(&cfg.throttleMS, "throttle", 40, "per-agent-step sleep floor in ms (bounds op rate)")
	flag.IntVar(&cfg.rssCapMB, "rss-cap", 3000, "abort load if server RSS exceeds this many MB (0=off)")
	flag.IntVar(&cfg.durCap, "dur-cap", 300, "per-board cap on tracked durable adds")
	flag.IntVar(&cfg.ephCap, "eph-cap", 25, "per-scope cap on ephemeral (deletable) items")
	flag.Parse()

	if cfg.db == "" {
		fatal("-db is required")
	}
	if cfg.reportPath == "" {
		cfg.reportPath = filepath.Join(filepath.Dir(cfg.db), "report.md")
	}
	cfg.logPath = filepath.Join(filepath.Dir(cfg.db), "server.log")

	h, err := newHarness(cfg)
	if err != nil {
		fatal(err.Error())
	}
	if err := h.run(); err != nil {
		h.report.fatalErr = err.Error()
		h.writeReport()
		fatal(err.Error())
	}
	h.writeReport()
	fmt.Println("soak complete; report:", cfg.reportPath)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "soak:", msg)
	os.Exit(1)
}

// ---- harness ----

type harness struct {
	cfg        config
	base       string // http://addr
	admin      string // admin bearer token
	client     *http.Client
	proc       *exec.Cmd
	boards     []*boardCfg
	report     *report
	crit       []string // CRITICAL findings
	critMu     sync.Mutex
	acked      map[string]*sync.Map     // board id -> set of acknowledged add payloads
	ackedCount map[string]*atomic.Int64 // board id -> durable-add count (for the cap)
	cancelLoad context.CancelFunc       // tripped by the RSS safety cap
	rssTripped atomic.Bool
	metrics    *metrics
}

// boardCfg is a created board plus the tokens/scopes minted on it.
type boardCfg struct {
	id       string
	rootIDs  []string     // seeded top-level item ids (subtree roots)
	sections []string     // section titles
	tokens   []*agentAuth // tokens minted for this board
}

type agentAuth struct {
	token string
	scope string // "" = whole-board write; else a subtree root id
	board *boardCfg
}

func newHarness(cfg config) (*harness, error) {
	h := &harness{
		cfg:        cfg,
		base:       "http://" + cfg.addr,
		client:     &http.Client{Timeout: 20 * time.Second},
		report:     &report{cfg: cfg, start: time.Now()},
		acked:      map[string]*sync.Map{},
		ackedCount: map[string]*atomic.Int64{},
		metrics:    &metrics{},
	}
	if cfg.bin == "" {
		bin := filepath.Join(filepath.Dir(cfg.db), "session-notes")
		if err := buildBinary(bin); err != nil {
			return nil, err
		}
		h.cfg.bin = bin
	}
	return h, nil
}

func buildBinary(out string) error {
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (h *harness) critical(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	h.critMu.Lock()
	h.crit = append(h.crit, msg)
	h.critMu.Unlock()
	fmt.Fprintln(os.Stderr, "CRITICAL:", msg)
}

func (h *harness) run() error {
	// 1. Fresh admin token in a fresh db, then start the server.
	os.Remove(h.cfg.db)
	os.Remove(h.cfg.db + "-wal")
	os.Remove(h.cfg.db + "-shm")
	tok, err := h.mintAdmin()
	if err != nil {
		return fmt.Errorf("mint admin token: %w", err)
	}
	h.admin = tok
	if err := h.startServer(); err != nil {
		return err
	}
	fmt.Println("server up:", h.base, "pid", h.proc.Process.Pid)

	// 2/3. Boards + scoped tokens.
	if err := h.setup(); err != nil {
		h.stopServer()
		return fmt.Errorf("setup: %w", err)
	}
	fmt.Printf("seeded %d boards, %d agent tokens\n", len(h.boards), h.tokenCount())

	// 4/5. Sustained load with periodic checkpoints.
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.dur)
	h.cancelLoad = cancel
	defer cancel()
	h.loadPhase(ctx)
	h.report.rssTripped = h.rssTripped.Load()

	// Final invariant sweep before shutdown.
	fmt.Println("load phase done; final pre-shutdown checkpoint")
	h.checkpoint("final-preshutdown")
	h.verifyAckedPresent("pre-shutdown")

	// 6. Graceful shutdown, restart on the same db, verify intact, one more round.
	fmt.Println("SIGTERM (graceful shutdown)")
	if err := h.stopServer(); err != nil {
		h.critical("graceful shutdown failed: %v", err)
	}
	preContents := h.report.lastBoardContents // captured by final checkpoint

	fmt.Println("restart on same db (crash-recovery sanity)")
	if err := h.startServer(); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	h.verifyPostRestart(preContents)
	h.checkpoint("post-restart")
	h.postRestartRound(ctx)
	h.verifyAckedPresent("post-restart")

	fmt.Println("final SIGTERM; leaving db stopped for forensics")
	_ = h.stopServer()
	h.report.end = time.Now()
	h.report.crit = h.crit
	return nil
}

func (h *harness) tokenCount() int {
	n := 0
	for _, b := range h.boards {
		n += len(b.tokens)
	}
	return n
}

// ---- server lifecycle ----

func (h *harness) mintAdmin() (string, error) {
	cmd := exec.Command(h.cfg.bin, "server", "token", "create", "--name", "soak-admin", "--db", h.cfg.db)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func (h *harness) startServer() error {
	logf, err := os.OpenFile(h.cfg.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(h.cfg.bin, "server", "--addr", h.cfg.addr, "--db", h.cfg.db)
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		return err
	}
	h.proc = cmd
	// Wait for healthz.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := h.client.Get(h.base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server did not become healthy within 15s")
}

func (h *harness) stopServer() error {
	if h.proc == nil || h.proc.Process == nil {
		return nil
	}
	p := h.proc.Process
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- h.proc.Wait() }()
	select {
	case err := <-done:
		h.proc = nil
		// Graceful shutdown exits 0; a non-zero/signal exit is a finding.
		if err != nil {
			return fmt.Errorf("server exit: %w", err)
		}
		return nil
	case <-time.After(12 * time.Second):
		_ = p.Kill()
		h.proc = nil
		return fmt.Errorf("server did not exit within 12s of SIGTERM (killed)")
	}
}

// ---- HTTP helpers ----

func (h *harness) do(method, path, token string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.base+path, r)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

type sectionJ struct {
	Title string     `json:"title"`
	ID    string     `json:"id"`
	Items []itemJSON `json:"items"`
}

type boardJSON struct {
	Sections []sectionJ `json:"sections"`
	Version  int        `json:"version"`
	Scope    string     `json:"scope"`
}

type itemJSON struct {
	ID       string     `json:"id"`
	Raw      string     `json:"raw"`
	Status   string     `json:"status"`
	Children []itemJSON `json:"children"`
}

func collectIDs(items []itemJSON, out *[]string) {
	for _, it := range items {
		if it.ID != "" {
			*out = append(*out, it.ID)
		}
		collectIDs(it.Children, out)
	}
}

// ---- setup ----

func (h *harness) setup() error {
	for i := 0; i < h.cfg.numBoards; i++ {
		id := fmt.Sprintf("soak-b%d", i)
		content := fmt.Sprintf("---\ntitle: Soak Board %d\n---\n\n## Threads\n\n- root alpha b%d\n- root bravo b%d\n\n## Log\n", i, i, i)
		st, _, err := h.do("POST", "/api/boards", h.admin, map[string]any{"id": id, "content": content})
		if err != nil {
			return err
		}
		if st != 201 {
			return fmt.Errorf("create board %s: status %d", id, st)
		}
		bc := &boardCfg{id: id, sections: []string{"Threads", "Log"}}
		// Read back to collect seeded item ids.
		st, data, err := h.do("GET", "/api/board/"+id, h.admin, nil)
		if err != nil || st != 200 {
			return fmt.Errorf("read board %s: status %d err %v", id, st, err)
		}
		var bj boardJSON
		if err := json.Unmarshal(data, &bj); err != nil {
			return err
		}
		for _, sec := range bj.Sections {
			var ids []string
			collectIDs(sec.Items, &ids)
			bc.rootIDs = append(bc.rootIDs, ids...)
		}
		if len(bc.rootIDs) < 2 {
			return fmt.Errorf("board %s: expected >=2 seeded ids, got %d", id, len(bc.rootIDs))
		}
		h.acked[id] = &sync.Map{}
		h.ackedCount[id] = &atomic.Int64{}

		// Two whole-board write tokens.
		for k := 0; k < 2; k++ {
			tok, err := h.grant(id, "", "write", fmt.Sprintf("wb-%s-%d", id, k))
			if err != nil {
				return err
			}
			bc.tokens = append(bc.tokens, &agentAuth{token: tok, scope: "", board: bc})
		}
		// Subtree-scoped write tokens: one per seeded root id.
		for k, root := range bc.rootIDs {
			tok, err := h.grant(id, root, "write", fmt.Sprintf("sc-%s-%d", id, k))
			if err != nil {
				return err
			}
			bc.tokens = append(bc.tokens, &agentAuth{token: tok, scope: root, board: bc})
		}
		h.boards = append(h.boards, bc)
	}
	return nil
}

// grant mints a new scoped token via the M4 newToken grant flow and returns it.
func (h *harness) grant(boardID, root, perm, name string) (string, error) {
	st, data, err := h.do("POST", "/api/board/"+boardID+"/grants", h.admin, map[string]any{
		"newToken": name, "root": root, "perm": perm,
	})
	if err != nil {
		return "", err
	}
	if st != 200 {
		return "", fmt.Errorf("grant on %s: status %d: %s", boardID, st, data)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("grant on %s returned no token", boardID)
	}
	return out.Token, nil
}

// ---- load phase ----

func (h *harness) loadPhase(ctx context.Context) {
	var wg sync.WaitGroup
	// Build the flat pool of (board, token) agent identities, then assign agents.
	var pool []*agentAuth
	for _, b := range h.boards {
		pool = append(pool, b.tokens...)
	}
	for i := 0; i < h.cfg.numAgents; i++ {
		aa := pool[i%len(pool)]
		wg.Add(1)
		go func(id int, aa *agentAuth) {
			defer wg.Done()
			h.agentLoop(ctx, id, aa)
		}(i, aa)
	}
	// Two admin agents: grants/revokes/export/history.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			h.adminLoop(ctx, id)
		}(i)
	}
	// Checkpoint ticker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(h.cfg.checkpoint)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.checkpoint("periodic")
			}
		}
	}()
	wg.Wait()
}

var payloadSeq atomic.Int64

func (h *harness) agentLoop(ctx context.Context, id int, aa *agentAuth) {
	rng := rand.New(rand.NewSource(int64(id)*7919 + time.Now().UnixNano()))
	author := fmt.Sprintf("agent-%d", id)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		h.agentStep(rng, id, author, aa)
		// throttle floor + jitter to bound op rate (and thus disk/RSS growth) and
		// avoid lockstep
		time.Sleep(time.Duration(h.cfg.throttleMS+rng.Intn(25)) * time.Millisecond)
	}
}

// agentStep performs one random surgical operation within the agent's scope.
func (h *harness) agentStep(rng *rand.Rand, id int, author string, aa *agentAuth) {
	// Read current board JSON (scoped tokens see only their subtree).
	st, data, err := h.do("GET", "/api/board/"+aa.board.id, aa.token, nil)
	if err != nil {
		h.metrics.readErr.Add(1)
		return
	}
	if st != 200 {
		h.metrics.readErr.Add(1)
		return
	}
	var bj boardJSON
	if err := json.Unmarshal(data, &bj); err != nil {
		h.metrics.readErr.Add(1)
		return
	}
	// Scoped view invariant: a scoped token must never see a sibling subtree.
	if aa.scope != "" {
		if bj.Scope != aa.scope {
			h.critical("scoped token on %s reports scope %q, want %q", aa.board.id, bj.Scope, aa.scope)
		}
	}
	// Ephemeral items (Raw prefix "eph") are deletable churn used to keep the
	// board bounded; durable adds (prefix "soak-add") are tracked and never
	// deleted, so the no-lost-write invariant stays sound.
	var ephIDs []string
	collectEphemeral(bj.Sections, &ephIDs)
	// Status/edit/contention ops target only the immutable seeded root ids, never
	// agent-added items. This keeps every acknowledged add payload verbatim in the
	// board so a missing payload is a genuine LOST WRITE, not a later overwrite.
	stable := h.stableTargets(aa)

	// Bound board growth: once a scope holds too many ephemeral items, drain the
	// overflow (batched, oldest-first) so board size converges to the cap and the
	// full-snapshot ops journal stays modest over a long run.
	if len(ephIDs) > h.cfg.ephCap {
		drain := len(ephIDs) - h.cfg.ephCap
		if drain > 100 {
			drain = 100
		}
		for i := 0; i < drain; i++ {
			h.opDelete(author, aa, ephIDs[i])
		}
		return
	}

	roll := rng.Intn(100)
	switch {
	case roll < 8:
		h.tryOutOfScope(rng, aa) // ~8%
	case roll < 18:
		h.watchOnce(aa) // ~10%
	case roll < 30:
		h.contendedPair(rng, author, aa, stable) // ~12% guaranteed-409 path
	case roll < 45:
		h.opAdd(rng, id, author, aa) // ~15% (growth; balanced by the drain path)
	case roll < 72:
		h.opStatus(rng, author, aa, stable) // ~27% (no growth)
	default:
		h.opSet(rng, author, aa, stable) // ~28% (no growth)
	}
}

func collectEphemeral(sections []sectionJ, out *[]string) {
	var walk func(items []itemJSON)
	walk = func(items []itemJSON) {
		for _, it := range items {
			// itemJSON.Raw keeps the bullet/status prefix (e.g. "- eph a2-9" or
			// "- [ ] eph ..."), so match the payload marker as a substring.
			if it.ID != "" && strings.Contains(it.Raw, "eph a") {
				*out = append(*out, it.ID)
			}
			walk(it.Children)
		}
	}
	for _, s := range sections {
		walk(s.Items)
	}
}

// opDelete removes an ephemeral churn item to bound board size.
func (h *harness) opDelete(author string, aa *agentAuth, id string) {
	h.metrics.deletes.Add(1)
	st, _, err := h.do("POST", "/api/board/"+aa.board.id+"/edit", aa.token, map[string]any{
		"op": "delete", "id": id, "author": author,
	})
	if st == 200 {
		h.metrics.deleteOK.Add(1)
	}
	h.classifyWrite(st, err, aa.board.id, "opDelete")
}

// stableTargets returns the node ids an agent may safely mutate (status/edit)
// without destroying tracked add payloads: the seeded roots. A scoped token owns
// exactly its scope root; a whole-board token owns all seeded roots.
func (h *harness) stableTargets(aa *agentAuth) []string {
	if aa.scope != "" {
		return []string{aa.scope}
	}
	return aa.board.rootIDs
}

// recordAck registers an acknowledged (200) add payload for final presence check.
func (h *harness) recordAck(boardID, payload string) {
	h.acked[boardID].Store(payload, true)
	h.ackedCount[boardID].Add(1)
	h.metrics.adds.Add(1)
}

// opAdd adds a new item (whole-board token → into Threads section; scoped token →
// as a child of its root). The payload is globally unique for the presence check.
func (h *harness) opAdd(rng *rand.Rand, id int, author string, aa *agentAuth) {
	seq := payloadSeq.Add(1)
	// Durable adds (tracked, never deleted) are capped per board so board size —
	// and thus the full-snapshot ops journal — stays bounded over a long run.
	// Beyond the cap, adds are ephemeral churn that the delete path later reaps.
	durable := h.ackedCount[aa.board.id].Load() < int64(h.cfg.durCap)
	var payload string
	if durable {
		payload = fmt.Sprintf("soak-add a%d-%d", id, seq)
	} else {
		payload = fmt.Sprintf("eph a%d-%d", id, seq)
	}
	body := map[string]any{"op": "add", "text": payload, "author": author}
	if aa.scope == "" {
		body["section"] = "Threads"
	}
	// scoped token: no section/id → "add" appends a child to the forced root.
	st, _, err := h.do("POST", "/api/board/"+aa.board.id+"/edit", aa.token, body)
	if err != nil {
		h.metrics.writeErr.Add(1)
		return
	}
	switch st {
	case 200:
		if durable {
			h.recordAck(aa.board.id, payload)
		} else {
			h.metrics.ephAdds.Add(1)
		}
	case 409:
		h.metrics.conflict.Add(1)
	default:
		if st >= 500 {
			h.metrics.serverErr.Add(1)
			h.critical("opAdd on %s got %d", aa.board.id, st)
		}
	}
}

func (h *harness) opStatus(rng *rand.Rand, author string, aa *agentAuth, ids []string) {
	if len(ids) == 0 {
		return
	}
	target := ids[rng.Intn(len(ids))]
	stat := []string{"open", "wip", "done", "blocked"}[rng.Intn(4)]
	st, _, err := h.do("POST", "/api/board/"+aa.board.id+"/edit", aa.token, map[string]any{
		"op": "status", "id": target, "status": stat, "author": author,
	})
	h.classifyWrite(st, err, aa.board.id, "opStatus")
}

func (h *harness) opSet(rng *rand.Rand, author string, aa *agentAuth, ids []string) {
	if len(ids) == 0 {
		return
	}
	target := ids[rng.Intn(len(ids))]
	seq := payloadSeq.Add(1)
	st, _, err := h.do("POST", "/api/board/"+aa.board.id+"/edit", aa.token, map[string]any{
		"op": "edit", "id": target, "text": fmt.Sprintf("edited-%d", seq), "author": author,
	})
	h.classifyWrite(st, err, aa.board.id, "opSet")
}

func (h *harness) classifyWrite(st int, err error, boardID, what string) {
	if err != nil {
		h.metrics.writeErr.Add(1)
		return
	}
	switch {
	case st == 200:
		h.metrics.writes.Add(1)
	case st == 409:
		h.metrics.conflict.Add(1)
	case st == 404:
		// node vanished (concurrent delete) — acceptable, ErrOpNotFound maps to 409/404
	case st >= 500:
		h.metrics.serverErr.Add(1)
		h.critical("%s on %s got %d", what, boardID, st)
	default:
		// 400/403 etc. — track so a systematically-rejected op is not silent.
		h.metrics.rejected.Add(1)
	}
}

// contendedPair provokes a deterministic 409 on the same node: op1 with base=v
// wins, op2 with the same stale base 409s, then a refetched retry succeeds. This
// exercises the M4 optimistic-base conflict path (no dropped write, no 5xx).
func (h *harness) contendedPair(rng *rand.Rand, author string, aa *agentAuth, ids []string) {
	if len(ids) == 0 {
		return
	}
	// current version
	st, data, err := h.do("GET", "/api/board/"+aa.board.id+"/raw", aa.token, nil)
	if err != nil || st != 200 {
		return
	}
	var raw struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return
	}
	target := ids[rng.Intn(len(ids))]
	base := raw.Version
	seq := payloadSeq.Add(1)
	// op1: should land (200)
	st1, _, _ := h.do("POST", "/api/board/"+aa.board.id+"/edit", aa.token, map[string]any{
		"op": "edit", "id": target, "text": fmt.Sprintf("cp1-%d", seq), "author": author, "base": base,
	})
	if st1 == 200 {
		h.metrics.writes.Add(1)
	} else if st1 == 409 {
		// someone else moved the version between GET and op1 — still fine
		h.metrics.conflict.Add(1)
		return
	} else if st1 >= 500 {
		h.metrics.serverErr.Add(1)
		h.critical("contendedPair op1 on %s got %d", aa.board.id, st1)
		return
	}
	// op2: stale base → must 409
	st2, d2, _ := h.do("POST", "/api/board/"+aa.board.id+"/edit", aa.token, map[string]any{
		"op": "edit", "id": target, "text": fmt.Sprintf("cp2-%d", seq), "author": author, "base": base,
	})
	if st2 != 409 {
		if st2 >= 500 {
			h.metrics.serverErr.Add(1)
		}
		h.critical("contendedPair op2 on %s expected 409 got %d", aa.board.id, st2)
		return
	}
	h.metrics.conflict.Add(1)
	// retry with fresh base from the 409 body
	var cj struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(d2, &cj)
	st3, _, _ := h.do("POST", "/api/board/"+aa.board.id+"/edit", aa.token, map[string]any{
		"op": "edit", "id": target, "text": fmt.Sprintf("cp3-%d", seq), "author": author, "base": cj.Version,
	})
	if st3 == 200 {
		h.metrics.retryOK.Add(1)
	} else if st3 >= 500 {
		h.metrics.serverErr.Add(1)
		h.critical("contendedPair retry on %s got %d", aa.board.id, st3)
	}
}

// tryOutOfScope has a scoped token attempt an op outside its subtree. Every such
// attempt MUST be refused (400/403/404). Any acknowledged mutation is CRITICAL.
func (h *harness) tryOutOfScope(rng *rand.Rand, aa *agentAuth) {
	if aa.scope == "" {
		return // whole-board token has no "outside"
	}
	h.metrics.oosAttempts.Add(1)
	// Pick a foreign target: a sibling root id on the same board, or another board.
	var foreignID, foreignBoard, explicitRoot string
	foreignBoard = aa.board.id
	for _, rid := range aa.board.rootIDs {
		if rid != aa.scope {
			foreignID = rid
			break
		}
	}
	mode := rng.Intn(3)
	var body map[string]any
	switch mode {
	case 0:
		// explicit root outside the grant root → 403
		explicitRoot = foreignID
		body = map[string]any{"op": "edit", "id": foreignID, "root": explicitRoot, "text": "OOS-root", "author": "oos"}
	case 1:
		// address a sibling id with no root (forced to grant root) → 400 scope check
		body = map[string]any{"op": "status", "id": foreignID, "status": "done", "author": "oos"}
	case 2:
		// a different board the token has no grant on → 404
		if len(h.boards) > 1 {
			for _, ob := range h.boards {
				if ob.id != aa.board.id {
					foreignBoard = ob.id
					if len(ob.rootIDs) > 0 {
						foreignID = ob.rootIDs[0]
					}
					break
				}
			}
		}
		body = map[string]any{"op": "status", "id": foreignID, "status": "done", "author": "oos"}
	}
	st, _, err := h.do("POST", "/api/board/"+foreignBoard+"/edit", aa.token, body)
	if err != nil {
		return
	}
	switch st {
	case 400, 403, 404:
		h.metrics.oosRefused.Add(1)
	case 200:
		h.critical("OUT-OF-SCOPE MUTATION ACCEPTED: token scope=%s board=%s target=%s explicitRoot=%s mode=%d -> 200",
			aa.scope, foreignBoard, foreignID, explicitRoot, mode)
	default:
		// unexpected but not a scope breach
		h.metrics.oosOther.Add(1)
	}
}

// watchOnce opens an SSE stream, waits briefly for one event or timeout, closes.
// Exercises subscriber open/close churn for the leak check.
func (h *harness) watchOnce(aa *agentAuth) {
	h.metrics.watches.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	url := h.base + "/api/board/" + aa.board.id + "/events?ignoreAuthor=nobody"
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+aa.token)
	resp, err := h.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			h.metrics.sseEvents.Add(1)
			return
		}
	}
}

// adminLoop does periodic grant/revoke/export/history as an admin identity.
func (h *harness) adminLoop(ctx context.Context, id int) {
	rng := rand.New(rand.NewSource(int64(id)*104729 + time.Now().UnixNano()))
	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(500+rng.Intn(1500)) * time.Millisecond):
		}
		i++
		b := h.boards[rng.Intn(len(h.boards))]
		switch i % 4 {
		case 0:
			// grant a throwaway token then revoke it
			name := fmt.Sprintf("throwaway-%d-%d", id, i)
			if _, err := h.grant(b.id, "", "read", name); err == nil {
				h.do("DELETE", "/api/board/"+b.id+"/grants", h.admin, map[string]any{"tokenName": name})
				h.metrics.adminOps.Add(1)
			}
		case 1:
			st, _, _ := h.do("GET", "/api/board/"+b.id+"/grants", h.admin, nil)
			if st == 200 {
				h.metrics.adminOps.Add(1)
			}
		case 2:
			st, _, _ := h.do("GET", "/api/board/"+b.id+"/history", h.admin, nil)
			if st == 200 {
				h.metrics.adminOps.Add(1)
			}
		case 3:
			// export via CLI (reads the live db over a second connection)
			dir := filepath.Join(filepath.Dir(h.cfg.db), "export")
			cmd := exec.Command(h.cfg.bin, "server", "export", "--dir", dir, "--db", h.cfg.db)
			if err := cmd.Run(); err == nil {
				h.metrics.adminOps.Add(1)
			} else {
				h.metrics.exportErr.Add(1)
			}
		}
	}
}

// ---- invariant checkpoints ----

func (h *harness) checkpoint(label string) {
	cp := checkpointResult{Label: label, At: time.Since(h.report.start)}

	// Resource sampling: RSS, healthz latency, established connections.
	cp.RSSkb = sampleRSS(h.proc)
	cp.HealthzMS, cp.HealthzOK = h.sampleHealthz()
	cp.Conns = sampleConns(h.proc, h.cfg.addr)

	// Host-safety: if RSS blows past the cap, stop the load phase (the growth is
	// still recorded in checkpoints for the report). Not a correctness failure.
	if h.cfg.rssCapMB > 0 && cp.RSSkb/1024 > h.cfg.rssCapMB && h.cancelLoad != nil {
		if h.rssTripped.CompareAndSwap(false, true) {
			fmt.Fprintf(os.Stderr, "RSS %d MB exceeded cap %d MB — stopping load phase\n", cp.RSSkb/1024, h.cfg.rssCapMB)
			h.cancelLoad()
		}
	}

	// Per-board correctness: parse round-trip + unique ids + monotonic version +
	// ops-rows == version-1.
	contents := map[string]string{}
	for _, b := range h.boards {
		st, data, err := h.do("GET", "/api/board/"+b.id+"/raw", h.admin, nil)
		if err != nil || st != 200 {
			h.critical("checkpoint %s: raw GET %s failed st=%d err=%v", label, b.id, st, err)
			continue
		}
		var raw struct {
			Content string `json:"content"`
			Version int    `json:"version"`
		}
		if json.Unmarshal(data, &raw) != nil {
			h.critical("checkpoint %s: raw JSON decode %s failed", label, b.id)
			continue
		}
		contents[b.id] = raw.Content
		// parse round-trip
		parsed := board.Parse(raw.Content)
		r1 := parsed.Render()
		r2 := board.Parse(r1).Render()
		if r1 != r2 {
			h.critical("checkpoint %s: board %s not render-stable (round-trip differs)", label, b.id)
		}
		// unique ids
		if dup := duplicateID(parsed); dup != "" {
			h.critical("checkpoint %s: board %s has duplicate id %q", label, b.id, dup)
		}
		// monotonic version
		prev := h.report.lastVersion[b.id]
		if raw.Version < prev {
			h.critical("checkpoint %s: board %s version regressed %d -> %d", label, b.id, prev, raw.Version)
		}
		// ops rows == version - 1 (board created at v1 with no ops row; each edit
		// bumps version by 1 and journals exactly one row).
		st, hd, herr := h.do("GET", "/api/board/"+b.id+"/history", h.admin, nil)
		if herr == nil && st == 200 {
			var hist struct {
				Entries []json.RawMessage `json:"entries"`
			}
			if json.Unmarshal(hd, &hist) == nil {
				wantRows := raw.Version - 1
				// Under live load /raw and /history are two separate reads, so a
				// write can land between them and history legitimately shows MORE
				// rows than version-1. A row DEFICIT (rows < version-1) would be a
				// genuine lost journal row. Strict equality is only asserted at a
				// quiescent checkpoint (agents stopped), where the two reads agree.
				quiescent := label != "periodic"
				if len(hist.Entries) < wantRows {
					h.critical("checkpoint %s: board %s ops-rows %d < version-1 %d (lost journal row)",
						label, b.id, len(hist.Entries), wantRows)
				} else if quiescent && len(hist.Entries) != wantRows {
					h.critical("checkpoint %s: board %s ops-rows %d != version-1 %d (quiescent)",
						label, b.id, len(hist.Entries), wantRows)
				}
				cp.TotalOps += len(hist.Entries)
			}
		}
		if h.report.lastVersion == nil {
			h.report.lastVersion = map[string]int{}
		}
		h.report.lastVersion[b.id] = raw.Version
	}
	h.report.lastBoardContents = contents
	h.report.checkpoints = append(h.report.checkpoints, cp)
	fmt.Printf("[checkpoint %s @%s] RSS=%dkb healthz=%.1fms conns=%d ops=%d adds=%d 409=%d retryOK=%d oos=%d/%d crit=%d\n",
		label, cp.At.Round(time.Second), cp.RSSkb, cp.HealthzMS, cp.Conns, cp.TotalOps,
		h.metrics.adds.Load(), h.metrics.conflict.Load(), h.metrics.retryOK.Load(),
		h.metrics.oosRefused.Load(), h.metrics.oosAttempts.Load(), len(h.crit))
}

func duplicateID(b *board.Board) string {
	seen := map[string]bool{}
	var dup string
	var walk func(items []*board.Item)
	walk = func(items []*board.Item) {
		for _, it := range items {
			if it.ID != "" {
				if seen[it.ID] {
					dup = it.ID
				}
				seen[it.ID] = true
			}
			walk(it.Children)
		}
	}
	for _, s := range b.Sections {
		if s.ID != "" {
			if seen[s.ID] {
				dup = s.ID
			}
			seen[s.ID] = true
		}
		walk(s.Items)
	}
	return dup
}

func (h *harness) sampleHealthz() (float64, bool) {
	start := time.Now()
	st, _, err := h.do("GET", "/healthz", "", nil)
	ms := float64(time.Since(start).Microseconds()) / 1000.0
	return ms, err == nil && st == 200
}

// verifyAckedPresent confirms every acknowledged (200) add payload is present in
// the final whole-board content — no acknowledged write silently lost.
func (h *harness) verifyAckedPresent(phase string) {
	missing := 0
	total := 0
	for _, b := range h.boards {
		st, data, err := h.do("GET", "/api/board/"+b.id+"/raw", h.admin, nil)
		if err != nil || st != 200 {
			h.critical("verifyAcked %s: cannot read %s", phase, b.id)
			continue
		}
		var raw struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(data, &raw)
		h.acked[b.id].Range(func(k, _ any) bool {
			total++
			if !strings.Contains(raw.Content, k.(string)) {
				missing++
				h.critical("verifyAcked %s: acknowledged add missing from %s: %q", phase, b.id, k.(string))
			}
			return true
		})
	}
	h.report.ackTotal = total
	h.report.ackMissing = missing
	fmt.Printf("[verifyAcked %s] %d acknowledged adds, %d missing\n", phase, total, missing)
}

// verifyPostRestart confirms every board's content survived the restart intact.
func (h *harness) verifyPostRestart(pre map[string]string) {
	for id, want := range pre {
		st, data, err := h.do("GET", "/api/board/"+id+"/raw", h.admin, nil)
		if err != nil || st != 200 {
			h.critical("post-restart: cannot read %s (st=%d)", id, st)
			continue
		}
		var raw struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(data, &raw)
		if raw.Content != want {
			h.critical("post-restart: board %s content changed across restart", id)
		}
		if board.Parse(raw.Content).Render() == "" {
			h.critical("post-restart: board %s no longer parses to content", id)
		}
	}
	h.report.restartVerified = true
	fmt.Println("[post-restart] all boards intact")
}

// postRestartRound runs a short burst of edits after restart to confirm the
// server accepts writes on the recovered db.
func (h *harness) postRestartRound(_ context.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var pool []*agentAuth
	for _, b := range h.boards {
		pool = append(pool, b.tokens...)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int, aa *agentAuth) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(id) + time.Now().UnixNano()))
			author := fmt.Sprintf("post-%d", id)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				h.opAdd(rng, 1000+id, author, aa)
				time.Sleep(10 * time.Millisecond)
			}
		}(i, pool[i%len(pool)])
	}
	wg.Wait()
	fmt.Println("[post-restart-round] complete")
}

// ---- resource sampling ----

func sampleRSS(proc *exec.Cmd) int {
	if proc == nil || proc.Process == nil {
		return 0
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(proc.Process.Pid)).Output()
	if err != nil {
		return 0
	}
	var kb int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &kb)
	return kb
}

func sampleConns(proc *exec.Cmd, addr string) int {
	if proc == nil || proc.Process == nil {
		return 0
	}
	out, err := exec.Command("lsof", "-nP", "-p", fmt.Sprint(proc.Process.Pid), "-iTCP").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "ESTABLISHED") {
			n++
		}
	}
	return n
}

// ---- metrics + report ----

type metrics struct {
	adds        atomic.Int64
	ephAdds     atomic.Int64
	writes      atomic.Int64
	conflict    atomic.Int64
	retryOK     atomic.Int64
	watches     atomic.Int64
	sseEvents   atomic.Int64
	oosAttempts atomic.Int64
	oosRefused  atomic.Int64
	oosOther    atomic.Int64
	adminOps    atomic.Int64
	readErr     atomic.Int64
	writeErr    atomic.Int64
	serverErr   atomic.Int64
	exportErr   atomic.Int64
	rejected    atomic.Int64
	deletes     atomic.Int64
	deleteOK    atomic.Int64
}

type checkpointResult struct {
	Label     string
	At        time.Duration
	RSSkb     int
	HealthzMS float64
	HealthzOK bool
	Conns     int
	TotalOps  int
}

type report struct {
	cfg               config
	start             time.Time
	end               time.Time
	checkpoints       []checkpointResult
	lastVersion       map[string]int
	lastBoardContents map[string]string
	ackTotal          int
	ackMissing        int
	restartVerified   bool
	rssTripped        bool
	crit              []string
	fatalErr          string
}

func (h *harness) writeReport() {
	r := h.report
	m := h.metrics
	var b strings.Builder
	fmt.Fprintf(&b, "# session-notes M8 soak report\n\n")
	fmt.Fprintf(&b, "- start: %s\n", r.start.Format(time.RFC3339))
	if !r.end.IsZero() {
		fmt.Fprintf(&b, "- end: %s (wall %s)\n", r.end.Format(time.RFC3339), r.end.Sub(r.start).Round(time.Second))
	}
	fmt.Fprintf(&b, "- config: boards=%d agents=%d dur=%s checkpoint=%s addr=%s\n",
		r.cfg.numBoards, r.cfg.numAgents, r.cfg.dur, r.cfg.checkpoint, r.cfg.addr)
	fmt.Fprintf(&b, "- db: %s\n\n", r.cfg.db)
	if r.fatalErr != "" {
		fmt.Fprintf(&b, "**FATAL: %s**\n\n", r.fatalErr)
	}

	fmt.Fprintf(&b, "## Totals\n\n")
	fmt.Fprintf(&b, "| metric | count |\n|---|---|\n")
	fmt.Fprintf(&b, "| acknowledged durable adds | %d |\n", m.adds.Load())
	fmt.Fprintf(&b, "| ephemeral (churn) adds | %d |\n", m.ephAdds.Load())
	fmt.Fprintf(&b, "| other 200 writes (status/edit/delete) | %d |\n", m.writes.Load())
	fmt.Fprintf(&b, "| 409 conflicts | %d |\n", m.conflict.Load())
	fmt.Fprintf(&b, "| retry-after-409 OK | %d |\n", m.retryOK.Load())
	fmt.Fprintf(&b, "| watches opened | %d |\n", m.watches.Load())
	fmt.Fprintf(&b, "| SSE events received | %d |\n", m.sseEvents.Load())
	fmt.Fprintf(&b, "| out-of-scope attempts | %d |\n", m.oosAttempts.Load())
	fmt.Fprintf(&b, "| out-of-scope refused | %d |\n", m.oosRefused.Load())
	fmt.Fprintf(&b, "| out-of-scope other-status | %d |\n", m.oosOther.Load())
	fmt.Fprintf(&b, "| delete-churn ops attempted | %d |\n", m.deletes.Load())
	fmt.Fprintf(&b, "| delete-churn ops 200 | %d |\n", m.deleteOK.Load())
	fmt.Fprintf(&b, "| ops rejected 4xx (non-404) | %d |\n", m.rejected.Load())
	fmt.Fprintf(&b, "| admin ops | %d |\n", m.adminOps.Load())
	fmt.Fprintf(&b, "| read errors | %d |\n", m.readErr.Load())
	fmt.Fprintf(&b, "| write errors (transport) | %d |\n", m.writeErr.Load())
	fmt.Fprintf(&b, "| 5xx server errors | %d |\n", m.serverErr.Load())
	fmt.Fprintf(&b, "| export errors | %d |\n\n", m.exportErr.Load())

	fmt.Fprintf(&b, "## Acknowledged-write durability\n\n")
	fmt.Fprintf(&b, "- acknowledged adds tracked: %d\n- missing from final board: %d\n\n", r.ackTotal, r.ackMissing)

	fmt.Fprintf(&b, "## Crash-recovery\n\n- boards verified intact post-restart: %v\n\n", r.restartVerified)

	if r.rssTripped {
		fmt.Fprintf(&b, "> NOTE: load phase was stopped early by the RSS safety cap (%d MB). See the growth trend below.\n\n", r.cfg.rssCapMB)
	}

	fmt.Fprintf(&b, "## Checkpoints (resource + correctness over time)\n\n")
	fmt.Fprintf(&b, "| label | t | RSS kb | healthz ms | healthz ok | est. conns | total ops |\n|---|---|---|---|---|---|---|\n")
	for _, c := range r.checkpoints {
		fmt.Fprintf(&b, "| %s | %s | %d | %.1f | %v | %d | %d |\n",
			c.Label, c.At.Round(time.Second), c.RSSkb, c.HealthzMS, c.HealthzOK, c.Conns, c.TotalOps)
	}
	b.WriteString("\n")

	// RSS/conn growth summary.
	if len(r.checkpoints) >= 2 {
		first, last := r.checkpoints[0], r.checkpoints[len(r.checkpoints)-1]
		fmt.Fprintf(&b, "- RSS delta first→last: %d → %d kb (%+d)\n", first.RSSkb, last.RSSkb, last.RSSkb-first.RSSkb)
		maxConn := 0
		for _, c := range r.checkpoints {
			if c.Conns > maxConn {
				maxConn = c.Conns
			}
		}
		fmt.Fprintf(&b, "- max established conns at any checkpoint: %d (agents=%d; SSE leak would grow unbounded)\n\n", maxConn, r.cfg.numAgents)
	}

	fmt.Fprintf(&b, "## CRITICAL findings\n\n")
	if len(r.crit) == 0 && len(h.crit) == 0 {
		fmt.Fprintf(&b, "None. All invariants held.\n")
	} else {
		crit := r.crit
		if len(crit) == 0 {
			crit = h.crit
		}
		// de-dup while preserving order
		seen := map[string]bool{}
		var uniq []string
		for _, c := range crit {
			if !seen[c] {
				seen[c] = true
				uniq = append(uniq, c)
			}
		}
		sort.SliceStable(uniq, func(i, j int) bool { return uniq[i] < uniq[j] })
		for _, c := range uniq {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}

	if err := os.WriteFile(r.cfg.reportPath, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "soak: write report:", err)
	}
}
