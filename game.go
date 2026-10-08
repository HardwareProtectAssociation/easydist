package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func (a *app) gameEntry(w http.ResponseWriter, r *http.Request) {
	origin, _ := url.Parse(a.c.BaseURL)
	dist := r.PathValue("dist")
	if !strings.EqualFold(r.Host, origin.Host) || !distPattern.MatchString(dist) {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	release, err := a.releaseTarget(filepath.Join(a.c.DeployDir, "public", dist))
	if err != nil {
		internal(w, err)
		return
	}
	if release == "" {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(filepath.Join(release, "index.html")); err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s</title><style>html,body{margin:0;width:100%%;height:100%%;overflow:hidden}iframe{width:100%%;height:100%%;border:0;display:block}</style></head><body><iframe title="%s" src="/_versions/%s/" allow="autoplay; fullscreen; gamepad" allowfullscreen></iframe></body></html>`, dist, dist, filepath.Base(release))
}
