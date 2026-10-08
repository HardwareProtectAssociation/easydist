package main

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type archiveError struct {
	message string
	status  int
}

func (e archiveError) Error() string { return e.message }
func badZIP(message string) error    { return archiveError{message, 400} }

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func extractZIP(ctx context.Context, archive, dest string, maxBytes int64, maxEntries int) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return badZIP("ZIP 文件无效")
	}
	defer z.Close()
	if len(z.File) > maxEntries {
		return archiveError{"ZIP 条目数量超限", 413}
	}
	type entry struct {
		name string
		file *zip.File
	}
	entries := []entry{}
	seen := map[string]bool{}
	roots := map[string]bool{}
	hasIndex := false
	var declared uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if name == "" || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
			return badZIP("ZIP 包含非法路径")
		}
		mode := f.Mode()
		if (!mode.IsRegular() && !mode.IsDir()) || (strings.HasSuffix(f.Name, "/") != mode.IsDir()) {
			return badZIP("ZIP 只能包含普通文件与目录")
		}
		if strings.Split(name, "/")[0] == "__MACOSX" || path.Base(name) == ".DS_Store" {
			continue
		}
		if seen[name] {
			return badZIP("ZIP 包含重复路径")
		}
		seen[name] = true
		if !mode.IsDir() {
			if f.UncompressedSize64 > uint64(maxBytes)-declared {
				return archiveError{"ZIP 解压大小超限", 413}
			}
			declared += f.UncompressedSize64
		}
		entries = append(entries, entry{name, f})
		roots[strings.Split(name, "/")[0]] = true
		if name == "index.html" && mode.IsRegular() {
			hasIndex = true
		}
	}
	prefix := ""
	if !hasIndex && len(roots) == 1 {
		for root := range roots {
			for _, e := range entries {
				if e.name == root+"/index.html" && e.file.Mode().IsRegular() {
					prefix = root + "/"
					hasIndex = true
				}
			}
		}
	}
	if !hasIndex {
		return badZIP("ZIP 根目录或唯一顶层目录必须包含 index.html")
	}
	var total int64
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := e.name
		if prefix != "" {
			if name == strings.TrimSuffix(prefix, "/") && e.file.Mode().IsDir() {
				continue
			}
			name = strings.TrimPrefix(name, prefix)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if e.file.Mode().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return badZIP("ZIP 文件与目录冲突")
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return badZIP("ZIP 文件与目录冲突")
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return badZIP("ZIP 文件与目录冲突")
			}
			return err
		}
		in, err := e.file.Open()
		if err != nil {
			out.Close()
			return badZIP("ZIP 文件损坏")
		}
		n, copyErr := io.Copy(out, io.LimitReader(contextReader{ctx, in}, maxBytes-total+1))
		in.Close()
		total += n
		if copyErr == nil {
			copyErr = out.Sync()
		}
		closeErr := out.Close()
		if total > maxBytes {
			return archiveError{"ZIP 解压大小超限", 413}
		}
		if copyErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(copyErr, zip.ErrChecksum) || errors.Is(copyErr, zip.ErrFormat) || errors.Is(copyErr, io.ErrUnexpectedEOF) {
				return badZIP("ZIP 文件损坏")
			}
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := precompress(ctx, dest); err != nil {
		return err
	}
	// Persist directory entries before publishing the link.
	return filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return syncDir(p)
		}
		return nil
	})
}

func precompress(ctx context.Context, dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".html", ".js", ".css", ".json", ".svg", ".wasm", ".pck":
		default:
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		original, err := in.Stat()
		if err != nil {
			return err
		}
		if info, err := os.Stat(p + ".gz"); err == nil && info.IsDir() {
			return badZIP("压缩文件路径与目录冲突")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		out, err := os.OpenFile(p+".gz", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		gz := gzip.NewWriter(out)
		_, copyErr := io.Copy(gz, contextReader{ctx, in})
		if err := errors.Join(copyErr, gz.Close(), out.Sync(), out.Close()); err != nil {
			return err
		}
		compressed, err := os.Stat(p + ".gz")
		if err != nil {
			return err
		}
		if compressed.Size() >= original.Size() {
			return os.Remove(p + ".gz")
		}
		return nil
	})
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	} // Production publication targets Linux containers.
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

var releasePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (a *app) releaseTarget(public string) (string, error) {
	link, err := os.Readlink(public)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(filepath.ToSlash(link), "../releases/")
	if !releasePattern.MatchString(name) || filepath.ToSlash(link) != "../releases/"+name {
		return "", errors.New("unexpected public symlink target")
	}
	return filepath.Join(a.c.DeployDir, "releases", name), nil
}
func (a *app) uploadToken(hash string) (token, error) {
	var t token
	err := a.db.QueryRow(`SELECT t.id,t.dist_name,t.created_at FROM tokens t JOIN users u ON u.id=t.user_id WHERE t.hash=? AND t.deleting=0 AND u.disabled=0`, hash).Scan(&t.ID, &t.DistName, &t.CreatedAt)
	if err == nil && !distPattern.MatchString(t.DistName) {
		err = errors.New("invalid stored dist name")
	}
	return t, err
}
func (a *app) deploy(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || len(strings.TrimPrefix(auth, "Bearer ")) != 43 {
		fail(w, 401, "上传 token 无效")
		return
	}
	hash := digest(strings.TrimPrefix(auth, "Bearer "))
	if _, err := a.uploadToken(hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 401, "上传 token 无效")
			return
		}
		internal(w, err)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/zip" {
		fail(w, 415, "Content-Type 必须为 application/zip")
		return
	}
	// ponytail: one deployment at a time; per-dist locks only if concurrent uploads become necessary.
	if !a.upload.TryLock() {
		fail(w, 409, "已有部署任务正在进行，请稍后重试")
		return
	}
	defer a.upload.Unlock()
	work, err := os.MkdirTemp(filepath.Join(a.c.DeployDir, "staging"), "upload-")
	if err != nil {
		internal(w, err)
		return
	}
	defer func() {
		if err := os.RemoveAll(work); err != nil {
			log.Print(err)
		}
	}()
	archive := filepath.Join(work, "package.zip")
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		internal(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.c.MaxZIP)
	_, copyErr := io.Copy(f, contextReader{r.Context(), r.Body})
	closeErr := f.Close()
	if copyErr != nil {
		var tooBig *http.MaxBytesError
		if errors.As(copyErr, &tooBig) {
			fail(w, 413, "ZIP 上传大小超限")
		} else {
			fail(w, 400, "上传未完成")
		}
		return
	}
	if closeErr != nil {
		internal(w, closeErr)
		return
	}
	content := filepath.Join(work, "content")
	if err := os.Mkdir(content, 0755); err != nil {
		internal(w, err)
		return
	}
	if err := extractZIP(r.Context(), archive, content, a.c.MaxExtract, a.c.MaxEntries); err != nil {
		var invalid archiveError
		if errors.As(err, &invalid) {
			fail(w, invalid.status, invalid.message)
		} else {
			internal(w, err)
		}
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.uploadToken(hash)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 401, "上传期间 token 已失效")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if err := r.Context().Err(); err != nil {
		fail(w, 400, "上传已取消")
		return
	}
	if err := a.publish(t, content, work); err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, map[string]string{"dist_name": t.DistName, "url": a.c.BaseURL + "/" + t.DistName + "/"})
}
func (a *app) publish(t token, content, work string) error {
	public := filepath.Join(a.c.DeployDir, "public", t.DistName)
	old, err := a.releaseTarget(public)
	if err != nil {
		return err
	}
	name, err := secret()
	if err != nil {
		return err
	}
	release := filepath.Join(a.c.DeployDir, "releases", name)
	if err := os.Rename(content, release); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			if err := os.RemoveAll(release); err != nil {
				log.Print(err)
			}
		}
	}()
	if err := syncDir(filepath.Dir(release)); err != nil {
		return err
	}
	link := filepath.Join(work, "link")
	if err := os.Symlink(filepath.FromSlash("../releases/"+name), link); err != nil {
		return err
	}
	if err := os.Rename(link, public); err != nil {
		return err
	}
	published = true
	if err := syncDir(filepath.Dir(public)); err != nil {
		return err
	}
	if old != "" {
		if err := os.RemoveAll(old); err != nil {
			log.Printf("old release cleanup: %v", err)
		}
	}
	return nil
}
func (a *app) recoverDeployments() error {
	rows, err := a.db.Query("SELECT id,dist_name FROM tokens WHERE deleting=1")
	if err != nil {
		return err
	}
	deleting := []token{}
	for rows.Next() {
		var t token
		if err := rows.Scan(&t.ID, &t.DistName); err != nil {
			rows.Close()
			return err
		}
		deleting = append(deleting, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, t := range deleting {
		if err := a.finishDelete(t); err != nil {
			return err
		}
	}
	refs := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(a.c.DeployDir, "public"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !distPattern.MatchString(e.Name()) {
			return fmt.Errorf("unexpected public entry %q", e.Name())
		}
		var n int
		if err := a.db.QueryRow("SELECT count(*) FROM tokens WHERE dist_name=?", e.Name()).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("public entry %q has no token", e.Name())
		}
		target, err := a.releaseTarget(filepath.Join(a.c.DeployDir, "public", e.Name()))
		if err != nil {
			return err
		}
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("release is not a directory")
		}
		refs[filepath.Base(target)] = true
	}
	for _, dir := range []string{"staging", "releases"} {
		entries, err := os.ReadDir(filepath.Join(a.c.DeployDir, dir))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if dir == "releases" && refs[e.Name()] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(a.c.DeployDir, dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
