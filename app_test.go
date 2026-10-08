package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testPassword = "a-long-test-password"

func testApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	a, err := openApp(config{DBPath: filepath.Join(dir, "data", "test.db"), DeployDir: filepath.Join(dir, "deploy"), WebDir: dir, AdminOrigin: "https://admin.example.test", BaseURL: "https://games.example.test", MaxZIP: 512 << 20, MaxExtract: 2 << 30, MaxEntries: 10000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	if err := a.bootstrap("admin", testPassword); err != nil {
		t.Fatal(err)
	}
	return a
}
func request(a *app, method, path string, body io.Reader, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("Origin", a.c.AdminOrigin)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
		r.Header.Set("Content-Type", "application/zip")
	} else {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}
func jsonRequest(a *app, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	return request(a, method, path, bytes.NewReader(b), cookie, "")
}
func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
}
func loginTest(t *testing.T, a *app, name, password string) *http.Cookie {
	t.Helper()
	w := jsonRequest(a, "POST", "/api/login", map[string]string{"username": name, "password": password}, nil)
	expect(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	return cookies[0]
}
func newUser(t *testing.T, a *app, admin *http.Cookie, name string) user {
	t.Helper()
	w := jsonRequest(a, "POST", "/api/admin/users", map[string]string{"username": name, "password": testPassword}, admin)
	expect(t, w, 201)
	var u user
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	return u
}
func newToken(t *testing.T, a *app, cookie *http.Cookie, name string) token {
	t.Helper()
	w := jsonRequest(a, "POST", "/api/tokens", map[string]string{"dist_name": name}, cookie)
	expect(t, w, 201)
	var tok token
	if err := json.Unmarshal(w.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

type zipEntry struct {
	name, body string
	mode       os.FileMode
}

func zipBytes(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		f, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func gameZIP(t *testing.T) []byte {
	return zipBytes(t, zipEntry{name: "index.html", body: "<html>game</html>"}, zipEntry{name: "game.wasm", body: "wasm"})
}

func TestAuthAndUsers(t *testing.T) {
	a := testApp(t)
	admin := loginTest(t, a, "admin", testPassword)
	if !admin.HttpOnly || !admin.Secure || admin.SameSite != http.SameSiteStrictMode || admin.Domain != "" || admin.MaxAge != 604800 {
		t.Fatalf("unsafe cookie: %+v", admin)
	}
	if err := a.bootstrap("another", "invalid"); err != nil {
		t.Fatal("bootstrap should not reset an existing DB", err)
	}
	alice := newUser(t, a, admin, "alice")
	newUser(t, a, admin, "bob")
	ac := loginTest(t, a, "alice", testPassword)
	bc := loginTest(t, a, "bob", testPassword)
	expect(t, jsonRequest(a, "GET", "/api/admin/users", nil, ac), 403)
	expect(t, jsonRequest(a, "GET", "/api/tokens", nil, nil), 401)
	tok := newToken(t, a, ac, "alice-game")
	expect(t, jsonRequest(a, "DELETE", fmt.Sprintf("/api/tokens/%d", tok.ID), nil, bc), 404)
	expect(t, jsonRequest(a, "POST", fmt.Sprintf("/api/tokens/%d/rotate", tok.ID), nil, bc), 404)
	expect(t, jsonRequest(a, "POST", "/api/tokens", map[string]string{"dist_name": "alice-game"}, bc), 409)
	r := httptest.NewRequest("POST", "/api/tokens", strings.NewReader(`{"dist_name":"bad-origin"}`))
	r.AddCookie(ac)
	r.Header.Set("Origin", a.c.BaseURL)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	expect(t, w, 403)
	r.Header.Del("Origin")
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	expect(t, w, 403)
	path := fmt.Sprintf("/api/admin/users/%d", alice.ID)
	expect(t, jsonRequest(a, "PATCH", path, map[string]bool{"disabled": true}, admin), 200)
	expect(t, jsonRequest(a, "GET", "/api/me", nil, ac), 401)
	expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(gameZIP(t)), nil, tok.Secret), 401)
	expect(t, jsonRequest(a, "POST", "/api/login", map[string]string{"username": "alice", "password": testPassword}, nil), 401)
	expect(t, jsonRequest(a, "PATCH", path, map[string]bool{"disabled": false}, admin), 200)
	ac = loginTest(t, a, "alice", testPassword)
	expect(t, jsonRequest(a, "POST", path+"/password", map[string]string{"password": "a-new-test-password"}, admin), 200)
	expect(t, jsonRequest(a, "GET", "/api/me", nil, ac), 401)
	expect(t, jsonRequest(a, "POST", "/api/login", map[string]string{"username": "alice", "password": testPassword}, nil), 401)
	ac = loginTest(t, a, "alice", "a-new-test-password")
	expect(t, jsonRequest(a, "POST", "/api/password", map[string]string{"current_password": "a-new-test-password", "password": "a-third-test-password"}, ac), 200)
	expect(t, jsonRequest(a, "GET", "/api/me", nil, ac), 401)
	loginTest(t, a, "alice", "a-third-test-password")
	expect(t, jsonRequest(a, "PATCH", "/api/admin/users/1", map[string]bool{"disabled": true}, admin), 400)
	list := jsonRequest(a, "GET", "/api/tokens", nil, bc)
	expect(t, list, 200)
	if strings.TrimSpace(list.Body.String()) != "[]" {
		t.Fatal("another user's tokens leaked")
	}
}

func TestTokenQuotaAndValidation(t *testing.T) {
	a := testApp(t)
	c := loginTest(t, a, "admin", testPassword)
	for _, name := range []string{"ab", "-abc", "abc-", "Upper", "a/b", "a..b", strings.Repeat("a", 49)} {
		expect(t, jsonRequest(a, "POST", "/api/tokens", map[string]string{"dist_name": name}, c), 400)
	}
	var wg sync.WaitGroup
	status := make(chan int, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status <- jsonRequest(a, "POST", "/api/tokens", map[string]string{"dist_name": fmt.Sprintf("game-%02d", i)}, c).Code
		}(i)
	}
	wg.Wait()
	close(status)
	created := 0
	for s := range status {
		if s == 201 {
			created++
		} else if s != 409 {
			t.Fatalf("unexpected status %d", s)
		}
	}
	if created != 16 {
		t.Fatalf("created %d tokens", created)
	}
	var stored string
	if err := a.db.QueryRow("SELECT hash FROM tokens LIMIT 1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 64 {
		t.Fatal("token not stored as SHA-256")
	}
}

func TestLoginThrottle(t *testing.T) {
	a := testApp(t)
	for i := 0; i < 10; i++ {
		expect(t, jsonRequest(a, "POST", "/api/login", map[string]string{"username": "nobody", "password": testPassword}, nil), 401)
	}
	expect(t, jsonRequest(a, "POST", "/api/login", map[string]string{"username": "nobody", "password": testPassword}, nil), 429)
}

func TestConfiguration(t *testing.T) {
	t.Setenv("ADMIN_ORIGIN", "https://admin.example.test")
	t.Setenv("BASE_URL", "https://games.example.test")
	t.Setenv("MAX_ZIP_BYTES", "536870912")
	t.Setenv("MAX_EXTRACT_BYTES", "2147483648")
	t.Setenv("MAX_ZIP_ENTRIES", "10000")
	if _, err := readConfig(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://admin.example.test:8443", "https://games.example.test/path", "https://user:pass@games.example.test", "file:///tmp/games"} {
		t.Setenv("BASE_URL", origin)
		if _, err := readConfig(); err == nil {
			t.Fatalf("unsafe origin accepted: %s", origin)
		}
	}
	t.Setenv("ADMIN_ORIGIN", "http://localhost:5173")
	t.Setenv("BASE_URL", "http://localhost:8081")
	if _, err := readConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAX_ZIP_BYTES", "-1")
	if _, err := readConfig(); err == nil {
		t.Fatal("negative upload limit accepted")
	}
}

func TestCancelledUpload(t *testing.T) {
	a := testApp(t)
	c := loginTest(t, a, "admin", testPassword)
	tok := newToken(t, a, c, "cancel-game")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", "/api/deploy", bytes.NewReader(gameZIP(t))).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+tok.Secret)
	r.Header.Set("Content-Type", "application/zip")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	expect(t, w, 400)
	entries, err := os.ReadDir(filepath.Join(a.c.DeployDir, "staging"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled staging not cleaned: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(a.c.DeployDir, "public", tok.DistName)); !os.IsNotExist(err) {
		t.Fatal("cancelled upload published")
	}
}

func TestZIPValidation(t *testing.T) {
	index := zipEntry{name: "index.html", body: "INDEX-CONTENT"}
	cases := []struct {
		name       string
		entries    []zipEntry
		maxBytes   int64
		maxEntries int
		ok         bool
	}{
		{"root", []zipEntry{index, {name: "game.wasm", body: "wasm"}}, 1024, 10, true},
		{"wrapped", []zipEntry{{name: "export/", mode: os.ModeDir | 0755}, {name: "export/index.html", body: "ok"}, {name: "export/nested/asset", body: "asset"}, {name: "__MACOSX/._index.html", body: "meta"}, {name: ".DS_Store", body: "meta"}}, 1024, 10, true},
		{"traversal", []zipEntry{index, {name: "../escape", body: "bad"}}, 1024, 10, false},
		{"absolute", []zipEntry{index, {name: "/escape", body: "bad"}}, 1024, 10, false},
		{"backslash", []zipEntry{index, {name: `dir\evil`, body: "bad"}}, 1024, 10, false},
		{"drive", []zipEntry{index, {name: "C:/evil", body: "bad"}}, 1024, 10, false},
		{"unclean", []zipEntry{index, {name: "dir/../evil", body: "bad"}}, 1024, 10, false},
		{"symlink", []zipEntry{index, {name: "link", body: "/etc/passwd", mode: os.ModeSymlink | 0777}}, 1024, 10, false},
		{"fifo", []zipEntry{index, {name: "pipe", mode: os.ModeNamedPipe | 0600}}, 1024, 10, false},
		{"duplicate", []zipEntry{index, index}, 1024, 10, false},
		{"directory-conflict", []zipEntry{index, {name: "asset", body: "file"}, {name: "asset/child", body: "child"}}, 1024, 10, false},
		{"reverse-conflict", []zipEntry{index, {name: "asset/child", body: "child"}, {name: "asset", body: "file"}}, 1024, 10, false},
		{"missing-index", []zipEntry{{name: "game.html", body: "html"}}, 1024, 10, false},
		{"two-roots", []zipEntry{{name: "export/index.html", body: "html"}, {name: "readme", body: "text"}}, 1024, 10, false},
		{"size-limit", []zipEntry{index}, 3, 10, false},
		{"entry-limit", []zipEntry{index, {name: "asset", body: "ok"}}, 1024, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "game.zip")
			if err := os.WriteFile(archive, zipBytes(t, tc.entries...), 0600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(dir, "out")
			os.Mkdir(dest, 0755)
			err := extractZIP(context.Background(), archive, dest, tc.maxBytes, tc.maxEntries)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected extraction result: %v", err)
			}
			if tc.ok {
				if _, err := os.Stat(filepath.Join(dest, "index.html")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("CRC", func(t *testing.T) {
		dir := t.TempDir()
		data := zipBytes(t, index)
		offset := bytes.Index(data, []byte("INDEX-CONTENT"))
		data[offset] = 'X'
		archive := filepath.Join(dir, "game.zip")
		os.WriteFile(archive, data, 0600)
		dest := filepath.Join(dir, "out")
		os.Mkdir(dest, 0755)
		if err := extractZIP(context.Background(), archive, dest, 1024, 10); err == nil {
			t.Fatal("CRC corruption accepted")
		}
	})
	t.Run("invalid", func(t *testing.T) {
		dir := t.TempDir()
		archive := filepath.Join(dir, "game.zip")
		os.WriteFile(archive, []byte("not a ZIP"), 0600)
		if err := extractZIP(context.Background(), archive, filepath.Join(dir, "out"), 1024, 10); err == nil {
			t.Fatal("invalid zip accepted")
		}
	})
}

func TestDeploymentPrecompression(t *testing.T) {
	a := testApp(t)
	c := loginTest(t, a, "admin", testPassword)
	tok := newToken(t, a, c, "compressed-game")
	public := filepath.Join(a.c.DeployDir, "public", tok.DistName)
	for _, body := range []string{strings.Repeat("old-game", 1024), strings.Repeat("new-game", 1024)} {
		archive := zipBytes(t, zipEntry{name: "index.html", body: body},
			zipEntry{name: "nested/game.wasm", body: body}, zipEntry{name: "game.pck", body: body},
			zipEntry{name: "game.pck.gz", body: "stale compressed data"},
			zipEntry{name: "small.js", body: "x"}, zipEntry{name: "image.png", body: body})
		expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(archive), nil, tok.Secret), 200)
		for _, name := range []string{"index.html", "nested/game.wasm", "game.pck"} {
			compressed, err := os.ReadFile(filepath.Join(public, name+".gz"))
			if err != nil || len(compressed) >= len(body) {
				t.Fatalf("missing or ineffective compression for %s: %v", name, err)
			}
			gz, err := gzip.NewReader(bytes.NewReader(compressed))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := io.ReadAll(gz)
			gz.Close()
			if err != nil || string(decoded) != body {
				t.Fatalf("compressed content differs for %s: %v", name, err)
			}
		}
		for _, name := range []string{"small.js.gz", "image.png.gz"} {
			if _, err := os.Stat(filepath.Join(public, name)); !os.IsNotExist(err) {
				t.Fatalf("unexpected compression for %s: %v", name, err)
			}
		}
	}
	conflict := zipBytes(t, zipEntry{name: "index.html", body: "replacement"}, zipEntry{name: "index.html.gz/", mode: os.ModeDir | 0755})
	expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(conflict), nil, tok.Secret), 400)
	if body, err := os.ReadFile(filepath.Join(public, "index.html")); err != nil || string(body) != strings.Repeat("new-game", 1024) {
		t.Fatalf("compression failure replaced existing game: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := precompress(ctx, public); err != context.Canceled {
		t.Fatalf("cancelled compression: %v", err)
	}
}

func TestDeploymentAndRecovery(t *testing.T) {
	a := testApp(t)
	c := loginTest(t, a, "admin", testPassword)
	tok := newToken(t, a, c, "game-one")
	first := zipBytes(t, zipEntry{name: "index.html", body: "old"}, zipEntry{name: "old.txt", body: "old"})
	expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(first), nil, tok.Secret), 200)
	public := filepath.Join(a.c.DeployDir, "public", tok.DistName)
	if err := a.recoverDeployments(); err != nil {
		t.Fatal(err)
	}
	expect(t, request(a, "POST", "/api/deploy", strings.NewReader("bad zip"), nil, tok.Secret), 400)
	if b, err := os.ReadFile(filepath.Join(public, "index.html")); err != nil || string(b) != "old" {
		t.Fatalf("failed upload changed old deployment: %v", err)
	}
	second := zipBytes(t, zipEntry{name: "export/index.html", body: "new"})
	expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(second), nil, tok.Secret), 200)
	if b, err := os.ReadFile(filepath.Join(public, "index.html")); err != nil || string(b) != "new" {
		t.Fatalf("replacement failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(public, "old.txt")); !os.IsNotExist(err) {
		t.Fatal("old file retained")
	}
	entries, _ := os.ReadDir(filepath.Join(a.c.DeployDir, "releases"))
	if len(entries) != 2 {
		t.Fatal("old release not retained")
	}
	staging, _ := os.ReadDir(filepath.Join(a.c.DeployDir, "staging"))
	if len(staging) != 0 {
		t.Fatal("staging retained")
	}
	w := jsonRequest(a, "POST", fmt.Sprintf("/api/tokens/%d/rotate", tok.ID), nil, c)
	expect(t, w, 200)
	var rotated token
	json.Unmarshal(w.Body.Bytes(), &rotated)
	if rotated.Secret == tok.Secret || !rotated.Deployed {
		t.Fatal("bad rotation")
	}
	expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(first), nil, tok.Secret), 401)
	expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(second), nil, rotated.Secret), 200)
	list := jsonRequest(a, "GET", "/api/tokens", nil, c)
	if strings.Contains(list.Body.String(), rotated.Secret) || strings.Contains(list.Body.String(), `"token"`) {
		t.Fatal("token secret leaked on list")
	}
	a.c.MaxZIP = 5
	expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(first), nil, rotated.Secret), 413)
	a.c.MaxZIP = 512 << 20
	keep := newToken(t, a, c, "game-keep")
	expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(first), nil, keep.Secret), 200)
	// Simulate a crash after marking deletion, plus unreferenced work.
	a.db.Exec("UPDATE tokens SET deleting=1 WHERE id=?", tok.ID)
	os.Mkdir(filepath.Join(a.c.DeployDir, "staging", "interrupted"), 0755)
	os.Mkdir(filepath.Join(a.c.DeployDir, "releases", "orphan"), 0755)
	a.db.Close()
	restarted, err := openApp(a.c)
	if err != nil {
		t.Fatal(err)
	}
	a = restarted
	t.Cleanup(func() { restarted.db.Close() })
	if err := a.recoverDeployments(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(public); !os.IsNotExist(err) {
		t.Fatal("pending deletion not recovered")
	}
	entries, _ = os.ReadDir(filepath.Join(a.c.DeployDir, "releases"))
	if len(entries) != 1 {
		t.Fatal("orphan releases not cleaned")
	}
	if b, err := os.ReadFile(filepath.Join(a.c.DeployDir, "public", keep.DistName, "index.html")); err != nil || string(b) != "old" {
		t.Fatalf("restart lost the current release: %v", err)
	}
	newTok := newToken(t, a, c, tok.DistName)
	expect(t, jsonRequest(a, "DELETE", fmt.Sprintf("/api/tokens/%d", newTok.ID), nil, c), 200)
}

type gatedReader struct {
	reader          io.Reader
	started, resume chan struct{}
	once            sync.Once
}

func (g *gatedReader) Read(p []byte) (int, error) {
	g.once.Do(func() { close(g.started); <-g.resume })
	return g.reader.Read(p)
}

func TestRevocationDuringUpload(t *testing.T) {
	for _, action := range []string{"rotate", "delete", "disable"} {
		t.Run(action, func(t *testing.T) {
			a := testApp(t)
			admin := loginTest(t, a, "admin", testPassword)
			u := newUser(t, a, admin, "alice")
			c := loginTest(t, a, "alice", testPassword)
			tok := newToken(t, a, c, "racing-game")
			data := gameZIP(t)
			g := &gatedReader{reader: bytes.NewReader(data), started: make(chan struct{}), resume: make(chan struct{})}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- request(a, "POST", "/api/deploy", g, nil, tok.Secret) }()
			<-g.started
			expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(data), nil, tok.Secret), 409)
			switch action {
			case "rotate":
				expect(t, jsonRequest(a, "POST", fmt.Sprintf("/api/tokens/%d/rotate", tok.ID), nil, c), 200)
			case "delete":
				expect(t, jsonRequest(a, "DELETE", fmt.Sprintf("/api/tokens/%d", tok.ID), nil, c), 200)
			case "disable":
				expect(t, jsonRequest(a, "PATCH", fmt.Sprintf("/api/admin/users/%d", u.ID), map[string]bool{"disabled": true}, admin), 200)
			}
			close(g.resume)
			expect(t, <-result, 401)
			if _, err := os.Lstat(filepath.Join(a.c.DeployDir, "public", tok.DistName)); !os.IsNotExist(err) {
				t.Fatal("revoked upload was published")
			}
		})
	}
}

func TestHourlyReleaseCleanup(t *testing.T) {
	a := testApp(t)
	cookie := loginTest(t, a, "admin", testPassword)
	tok := newToken(t, a, cookie, "game-cleanup")
	var releases []string
	for _, body := range []string{"expired", "recent", "current"} {
		archive := zipBytes(t, zipEntry{name: "index.html", body: body})
		expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(archive), nil, tok.Secret), 200)
		release, err := a.releaseTarget(filepath.Join(a.c.DeployDir, "public", tok.DistName))
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	for i, age := range []time.Duration{8 * 24 * time.Hour, 6 * 24 * time.Hour} {
		if _, err := a.db.Exec("UPDATE releases SET retired_at=? WHERE name=?", time.Now().Add(-age).Unix(), filepath.Base(releases[i])); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.cleanupReleases(ctx, ticks)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("cleanup did not stop on cancellation")
		}
	})
	func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		select {
		case ticks <- time.Now():
		case <-time.After(3 * time.Second):
			t.Fatal("cleanup did not accept scheduled tick")
		}
		if _, err := os.Stat(releases[0]); err != nil {
			t.Fatalf("cleanup bypassed mutation lock: %v", err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := a.db.QueryRow("SELECT count(*) FROM releases WHERE name=?", filepath.Base(releases[0])).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduled cleanup did not remove expired record")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(releases[0]); !os.IsNotExist(err) {
		t.Fatalf("scheduled cleanup left expired files: %v", err)
	}
	for i, body := range []string{"recent", "current"} {
		data, err := os.ReadFile(filepath.Join(releases[i+1], "index.html"))
		if err != nil || string(data) != body {
			t.Fatalf("cleanup changed %s release: %v", body, err)
		}
	}
}

func TestVersionedGameEntryAndRetention(t *testing.T) {
	a := testApp(t)
	cookie := loginTest(t, a, "admin", testPassword)
	tok := newToken(t, a, cookie, "game-cache")
	entry := func(host, dist string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://"+host+"/game/"+dist, nil)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		return w
	}
	expect(t, entry("games.example.test", tok.DistName), 404)
	upload := func(body string) string {
		archive := zipBytes(t, zipEntry{name: "index.html", body: body}, zipEntry{name: "index.pck", body: body})
		expect(t, request(a, "POST", "/api/deploy", bytes.NewReader(archive), nil, tok.Secret), 200)
		release, err := a.releaseTarget(filepath.Join(a.c.DeployDir, "public", tok.DistName))
		if err != nil {
			t.Fatal(err)
		}
		w := entry("games.example.test", tok.DistName)
		expect(t, w, 200)
		if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `src="/_versions/`+filepath.Base(release)+`/"`) {
			t.Fatalf("entry does not pin current release: %s", w.Body.String())
		}
		return release
	}
	first := upload("old")
	// Existing installations have no version records; startup must adopt their current release.
	if _, err := a.db.Exec("DELETE FROM releases"); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverDeployments(); err != nil {
		t.Fatal(err)
	}
	second := upload("new")
	if first == second {
		t.Fatal("replacement reused a version URL")
	}
	if body, err := os.ReadFile(filepath.Join(first, "index.pck")); err != nil || string(body) != "old" {
		t.Fatalf("replacement lost old resources: %v", err)
	}
	expect(t, entry("admin.example.test", tok.DistName), 404)
	expect(t, entry("games.example.test", "bad_name"), 404)
	expect(t, entry("games.example.test", "unknown-game"), 404)
	// Recover a crash after link replacement but before recording retirement.
	if _, err := a.db.Exec("UPDATE releases SET retired_at=0 WHERE name=?", filepath.Base(first)); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverDeployments(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("restart deleted retained version: %v", err)
	}
	if _, err := a.db.Exec("UPDATE releases SET retired_at=? WHERE name=?", time.Now().Add(-7*24*time.Hour).Unix(), filepath.Base(first)); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverDeployments(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("expired version not removed: %v", err)
	}
	upload("third")
	expect(t, jsonRequest(a, "DELETE", fmt.Sprintf("/api/tokens/%d", tok.ID), nil, cookie), 200)
	expect(t, entry("games.example.test", tok.DistName), 404)
	entries, err := os.ReadDir(filepath.Join(a.c.DeployDir, "releases"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("token deletion left versions: %v", err)
	}
}
