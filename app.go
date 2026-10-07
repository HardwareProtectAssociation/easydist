package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var distPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,46}[a-z0-9])$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{2,31}$`)

type app struct {
	c  config
	db *sql.DB
	// ponytail: single-instance mutation lock; use DB transactions across instances if scaling is needed.
	mu       sync.Mutex
	upload   sync.Mutex
	rateMu   sync.Mutex
	attempts map[string]attempt
}
type attempt struct {
	count int
	until time.Time
}
type user struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Admin    bool   `json:"admin"`
	Disabled bool   `json:"disabled"`
}

func openApp(c config) (*app, error) {
	if err := os.MkdirAll(filepath.Dir(c.DBPath), 0700); err != nil {
		return nil, err
	}
	for _, dir := range []string{"staging", "releases", "public"} {
		if err := os.MkdirAll(filepath.Join(c.DeployDir, dir), 0755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", c.DBPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE, password TEXT NOT NULL, admin INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS tokens(id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL REFERENCES users(id), dist_name TEXT NOT NULL UNIQUE, hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, deleting INTEGER NOT NULL DEFAULT 0);
DELETE FROM sessions WHERE expires <= unixepoch();`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(c.DBPath, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &app{c: c, db: db, attempts: make(map[string]attempt)}, nil
}

func passwordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 72 {
		return "", errors.New("密码必须为 12–72 字节")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}
func (a *app) bootstrap(username, password string) error {
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if !userPattern.MatchString(username) {
		return errors.New("initial ADMIN_USERNAME must be 3–32 ASCII letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	_, err = a.db.Exec("INSERT INTO users(username,password,admin) VALUES(?,?,1)", username, hash)
	return err
}
func secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("response: %v", err)
	}
}
func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}
func internal(w http.ResponseWriter, err error) {
	log.Printf("operation failed: %v", err)
	fail(w, 500, "操作失败，请查看服务日志")
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "JSON 请求无效")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "JSON 请求无效")
		return false
	}
	return true
}
func (a *app) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			fail(w, 503, "数据库不可用")
			return
		}
		reply(w, 200, map[string]string{"status": "ok"})
	})
	m.HandleFunc("POST /api/login", a.login)
	m.HandleFunc("POST /api/logout", a.withUser(a.logout))
	m.HandleFunc("GET /api/me", a.withUser(func(w http.ResponseWriter, r *http.Request, u user) { reply(w, 200, u) }))
	m.HandleFunc("POST /api/password", a.withUser(a.changePassword))
	m.HandleFunc("GET /api/tokens", a.withUser(a.listTokens))
	m.HandleFunc("POST /api/tokens", a.withUser(a.createToken))
	m.HandleFunc("DELETE /api/tokens/{id}", a.withUser(a.deleteToken))
	m.HandleFunc("POST /api/tokens/{id}/rotate", a.withUser(a.rotateToken))
	m.HandleFunc("GET /api/admin/users", a.withUser(a.listUsers))
	m.HandleFunc("POST /api/admin/users", a.withUser(a.createUser))
	m.HandleFunc("POST /api/admin/users/{id}/password", a.withUser(a.resetPassword))
	m.HandleFunc("PATCH /api/admin/users/{id}", a.withUser(a.setDisabled))
	m.HandleFunc("POST /api/deploy", a.deploy)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	m.Handle("/", http.FileServer(http.Dir(a.c.WebDir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		// SvelteKit emits the script hashes in a CSP meta tag for the static build.
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/deploy" && r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != a.c.AdminOrigin {
			fail(w, 403, "请求来源无效")
			return
		}
		m.ServeHTTP(w, r)
	})
}
