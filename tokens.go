package main

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type token struct {
	ID         int64  `json:"id"`
	DistName   string `json:"dist_name"`
	CreatedAt  int64  `json:"created_at"`
	Deleting   bool   `json:"deleting"`
	URL        string `json:"url"`
	Deployed   bool   `json:"deployed"`
	DeployedAt int64  `json:"deployed_at,omitempty"`
	Secret     string `json:"token,omitempty"`
}

func (a *app) describe(t *token) error {
	t.URL = a.c.BaseURL + "/" + t.DistName + "/"
	info, err := os.Lstat(filepath.Join(a.c.DeployDir, "public", t.DistName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	t.Deployed = info.Mode()&os.ModeSymlink != 0
	if t.Deployed {
		t.DeployedAt = info.ModTime().Unix()
	}
	return nil
}
func (a *app) listTokens(w http.ResponseWriter, r *http.Request, u user) {
	rows, err := a.db.Query("SELECT id,dist_name,created_at,deleting FROM tokens WHERE user_id=? ORDER BY id DESC", u.ID)
	if err != nil {
		internal(w, err)
		return
	}
	defer rows.Close()
	tokens := []token{}
	for rows.Next() {
		var t token
		if err := rows.Scan(&t.ID, &t.DistName, &t.CreatedAt, &t.Deleting); err != nil {
			internal(w, err)
			return
		}
		if err := a.describe(&t); err != nil {
			internal(w, err)
			return
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, tokens)
}
func (a *app) createToken(w http.ResponseWriter, r *http.Request, u user) {
	var in struct {
		DistName string `json:"dist_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !distPattern.MatchString(in.DistName) {
		fail(w, 400, "dist name 须为 3–48 位小写字母、数字或连字符，首尾不能为连字符")
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
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM tokens WHERE user_id=?", u.ID).Scan(&n); err != nil {
		internal(w, err)
		return
	}
	if n >= 16 {
		fail(w, 409, "每个用户最多创建 16 个 token")
		return
	}
	if err := tx.QueryRow("SELECT count(*) FROM tokens WHERE dist_name=?", in.DistName).Scan(&n); err != nil {
		internal(w, err)
		return
	}
	if n > 0 {
		fail(w, 409, "dist name 已被占用")
		return
	}
	t := token{DistName: in.DistName, CreatedAt: time.Now().Unix(), Secret: s}
	result, err := tx.Exec("INSERT INTO tokens(user_id,dist_name,hash,created_at) VALUES(?,?,?,?)", u.ID, t.DistName, digest(s), t.CreatedAt)
	if err == nil {
		t.ID, err = result.LastInsertId()
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		internal(w, err)
		return
	}
	t.URL = a.c.BaseURL + "/" + t.DistName + "/"
	reply(w, 201, t)
}
func (a *app) ownedToken(w http.ResponseWriter, r *http.Request, u user) (token, bool) {
	var t token
	err := a.db.QueryRow("SELECT id,dist_name,created_at,deleting FROM tokens WHERE id=? AND user_id=?", r.PathValue("id"), u.ID).Scan(&t.ID, &t.DistName, &t.CreatedAt, &t.Deleting)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "token 不存在")
		return t, false
	}
	if err != nil {
		internal(w, err)
		return t, false
	}
	return t, true
}
func (a *app) rotateToken(w http.ResponseWriter, r *http.Request, u user) {
	t, ok := a.ownedToken(w, r, u)
	if !ok {
		return
	}
	if t.Deleting {
		fail(w, 409, "部署正在删除")
		return
	}
	s, err := secret()
	if err != nil {
		internal(w, err)
		return
	}
	if _, err := a.db.Exec("UPDATE tokens SET hash=? WHERE id=?", digest(s), t.ID); err != nil {
		internal(w, err)
		return
	}
	t.Secret = s
	if err := a.describe(&t); err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, t)
}
func (a *app) deleteToken(w http.ResponseWriter, r *http.Request, u user) {
	t, ok := a.ownedToken(w, r, u)
	if !ok {
		return
	}
	if _, err := a.db.Exec("UPDATE tokens SET deleting=1 WHERE id=?", t.ID); err != nil {
		internal(w, err)
		return
	}
	if err := a.finishDelete(t); err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *app) finishDelete(t token) error {
	if !distPattern.MatchString(t.DistName) {
		return errors.New("invalid stored dist name")
	}
	public := filepath.Join(a.c.DeployDir, "public", t.DistName)
	target, err := a.releaseTarget(public)
	if err != nil {
		return err
	}
	rows, err := a.db.Query("SELECT name FROM releases WHERE dist_name=?", t.DistName)
	if err != nil {
		return err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !releasePattern.MatchString(name) {
			rows.Close()
			return errors.New("invalid stored release name")
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := os.RemoveAll(filepath.Join(a.c.DeployDir, "releases", name)); err != nil {
			return err
		}
	}
	if target != "" {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	if err := os.Remove(public); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDir(filepath.Dir(public)); err != nil {
		return err
	}
	_, err = a.db.Exec("DELETE FROM tokens WHERE id=? AND deleting=1", t.ID)
	return err
}
