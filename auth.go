package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const cookieName = "easydist_session"

func (a *app) cookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: strings.HasPrefix(a.c.AdminOrigin, "https://"), SameSite: http.SameSiteStrictMode})
}
func (a *app) session(r *http.Request) (user, error) {
	var u user
	c, err := r.Cookie(cookieName)
	if err != nil {
		return u, sql.ErrNoRows
	}
	err = a.db.QueryRow(`SELECT u.id,u.username,u.admin,u.disabled FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.hash=? AND s.expires>? AND u.disabled=0`, digest(c.Value), time.Now().Unix()).Scan(&u.ID, &u.Username, &u.Admin, &u.Disabled)
	return u, err
}
func (a *app) withUser(fn func(http.ResponseWriter, *http.Request, user)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		u, err := a.session(r)
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 401, "请重新登录")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		fn(w, r, u)
	}
}
func (a *app) allowLogin(username string) bool {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	now := time.Now()
	for key, v := range a.attempts {
		if !now.Before(v.until) {
			delete(a.attempts, key)
		}
	}
	for _, rule := range []struct {
		key      string
		limit    int
		duration time.Duration
	}{{"global", 60, time.Minute}, {"user:" + username, 10, 15 * time.Minute}} {
		v := a.attempts[rule.key]
		if v.count >= rule.limit {
			return false
		}
		if v.count == 0 {
			v.until = now.Add(rule.duration)
		}
		v.count++
		a.attempts[rule.key] = v
	}
	return true
}
func (a *app) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !a.allowLogin(in.Username) {
		w.Header().Set("Retry-After", "900")
		fail(w, 429, "登录尝试过于频繁，请稍后再试")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var u user
	var hash string
	err := a.db.QueryRow("SELECT id,username,admin,disabled,password FROM users WHERE username=?", in.Username).Scan(&u.ID, &u.Username, &u.Admin, &u.Disabled, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		internal(w, err)
		return
	}
	// Equal-cost verification also for unknown usernames.
	if hash == "" {
		hash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	}
	passwordErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password))
	if err != nil || u.Disabled || len(in.Password) > 72 || passwordErr != nil {
		fail(w, 401, "用户名或密码错误")
		return
	}
	s, err := secret()
	if err != nil {
		internal(w, err)
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM sessions WHERE expires<=?", time.Now().Unix()); err == nil {
		_, err = tx.Exec("INSERT INTO sessions(hash,user_id,expires) VALUES(?,?,?)", digest(s), u.ID, time.Now().Add(7*24*time.Hour).Unix())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		internal(w, err)
		return
	}
	a.cookie(w, s, 7*24*3600)
	reply(w, 200, u)
}
func (a *app) logout(w http.ResponseWriter, r *http.Request, u user) {
	c, _ := r.Cookie(cookieName)
	if _, err := a.db.Exec("DELETE FROM sessions WHERE hash=?", digest(c.Value)); err != nil {
		internal(w, err)
		return
	}
	a.cookie(w, "", -1)
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *app) updatePassword(id int64, hash string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE users SET password=? WHERE id=?", hash, id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *app) changePassword(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		Current  string `json:"current_password"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	var old string
	if err := a.db.QueryRow("SELECT password FROM users WHERE id=?", u.ID).Scan(&old); err != nil {
		internal(w, err)
		return
	}
	if len(in.Current) > 72 || bcrypt.CompareHashAndPassword([]byte(old), []byte(in.Current)) != nil {
		fail(w, 400, "当前密码错误")
		return
	}
	hash, err := passwordHash(in.Password)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := a.updatePassword(u.ID, hash); err != nil {
		internal(w, err)
		return
	}
	a.cookie(w, "", -1)
	reply(w, 200, map[string]bool{"ok": true})
}
func requireAdmin(w http.ResponseWriter, u user) bool {
	if !u.Admin {
		fail(w, 403, "需要管理员权限")
		return false
	}
	return true
}
func (a *app) listUsers(w http.ResponseWriter, r *http.Request, u user) {
	if !requireAdmin(w, u) {
		return
	}
	rows, err := a.db.Query("SELECT id,username,admin,disabled FROM users ORDER BY id")
	if err != nil {
		internal(w, err)
		return
	}
	defer rows.Close()
	users := []user{}
	for rows.Next() {
		var v user
		if err := rows.Scan(&v.ID, &v.Username, &v.Admin, &v.Disabled); err != nil {
			internal(w, err)
			return
		}
		users = append(users, v)
	}
	if err := rows.Err(); err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, users)
}
func (a *app) createUser(w http.ResponseWriter, r *http.Request, u user) {
	if !requireAdmin(w, u) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !userPattern.MatchString(in.Username) {
		fail(w, 400, "用户名须为 3–32 位字母、数字、下划线或连字符，并以字母或数字开头")
		return
	}
	hash, err := passwordHash(in.Password)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var exists int
	if err := a.db.QueryRow("SELECT count(*) FROM users WHERE username=?", in.Username).Scan(&exists); err != nil {
		internal(w, err)
		return
	}
	if exists > 0 {
		fail(w, 409, "用户名已存在")
		return
	}
	result, err := a.db.Exec("INSERT INTO users(username,password) VALUES(?,?)", in.Username, hash)
	if err != nil {
		internal(w, err)
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		internal(w, err)
		return
	}
	reply(w, 201, user{ID: id, Username: in.Username})
}
func (a *app) targetUser(w http.ResponseWriter, r *http.Request) (user, bool) {
	var v user
	err := a.db.QueryRow("SELECT id,username,admin,disabled FROM users WHERE id=?", r.PathValue("id")).Scan(&v.ID, &v.Username, &v.Admin, &v.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "用户不存在")
		return v, false
	}
	if err != nil {
		internal(w, err)
		return v, false
	}
	return v, true
}
func (a *app) resetPassword(w http.ResponseWriter, r *http.Request, u user) {
	if !requireAdmin(w, u) {
		return
	}
	target, ok := a.targetUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	hash, err := passwordHash(in.Password)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := a.updatePassword(target.ID, hash); err != nil {
		internal(w, err)
		return
	}
	if target.ID == u.ID {
		a.cookie(w, "", -1)
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *app) setDisabled(w http.ResponseWriter, r *http.Request, u user) {
	if !requireAdmin(w, u) {
		return
	}
	target, ok := a.targetUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Disabled *bool `json:"disabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Disabled == nil {
		fail(w, 400, "缺少 disabled 字段")
		return
	}
	if target.Admin {
		fail(w, 400, "不能禁用管理员")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE users SET disabled=? WHERE id=?", *in.Disabled, target.ID)
	if err == nil && *in.Disabled {
		_, err = tx.Exec("DELETE FROM sessions WHERE user_id=?", target.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
