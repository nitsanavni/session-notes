package cloud

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// getJSON does an authenticated GET and decodes the JSON body, returning the
// status code.
func getJSON(t *testing.T, url, tok string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// TestMeEndpoint: the browser feature-detects the cloud, admin-only share
// action via GET /api/me. An admin token reports admin:true; a scoped
// (non-admin) token reports its subject and admin:false; no token 401s.
func TestMeEndpoint(t *testing.T) {
	s, ts := testServer(t, false)
	admin, _ := s.CreateToken("boss")
	scoped, _ := s.CreateTokenAs("scout", "scout", false)

	var me struct {
		Subject string `json:"subject"`
		Admin   bool   `json:"admin"`
		Cloud   bool   `json:"cloud"`
	}
	if code := getJSON(t, ts.URL+"/api/me", admin, &me); code != http.StatusOK {
		t.Fatalf("admin /api/me status=%d", code)
	}
	if !me.Admin || me.Subject != "boss" || !me.Cloud {
		t.Fatalf("admin me = %+v", me)
	}

	me = struct {
		Subject string `json:"subject"`
		Admin   bool   `json:"admin"`
		Cloud   bool   `json:"cloud"`
	}{}
	if code := getJSON(t, ts.URL+"/api/me", scoped, &me); code != http.StatusOK {
		t.Fatalf("scoped /api/me status=%d", code)
	}
	if me.Admin || me.Subject != "scout" || !me.Cloud {
		t.Fatalf("scoped me = %+v", me)
	}

	if code := getJSON(t, ts.URL+"/api/me", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no-token /api/me status=%d want 401", code)
	}
}

// TestShareGrantEndpoint exercises the endpoint the web "share node" action
// drives: POST /api/board/{id}/grants with a minted token scoped to a node.
// Admin succeeds and gets back a usable token + the grant; a non-admin token
// is refused 403.
func TestShareGrantEndpoint(t *testing.T) {
	s, ts := testServer(t, false)
	admin, _ := s.CreateToken("boss")
	scoped, _ := s.CreateTokenAs("scout", "scout", false)
	node := threadsID(t, s)

	body, _ := json.Marshal(map[string]string{"newToken": "shared-1", "root": node, "perm": "write"})

	// Non-admin: refused.
	req, _ := http.NewRequest("POST", ts.URL+"/api/board/b1/grants", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+scoped)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin share status=%d want 403", resp.StatusCode)
	}

	// Admin: mints a scoped token + grant.
	req, _ = http.NewRequest("POST", ts.URL+"/api/board/b1/grants", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin share status=%d want 200", resp.StatusCode)
	}
	var out struct {
		Subject string `json:"subject"`
		Perm    string `json:"perm"`
		Root    string `json:"root"`
		Token   string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" || out.Root != node || out.Perm != "write" {
		t.Fatalf("share result = %+v", out)
	}

	// The minted token resolves to a non-admin identity scoped to the node.
	id, ok := s.TokenIdentity(out.Token)
	if !ok || id.Admin {
		t.Fatalf("minted token identity = %+v ok=%v", id, ok)
	}
	g, ok := s.GrantFor(id.Subject, "b1")
	if !ok || g.Root != node || g.Perm != "write" {
		t.Fatalf("minted grant = %+v ok=%v", g, ok)
	}
}
