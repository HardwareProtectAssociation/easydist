package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type config struct {
	DBPath, DeployDir, WebDir, AdminOrigin, BaseURL, Listen string
	MaxZIP, MaxExtract                                      int64
	MaxEntries                                              int
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func readConfig() (config, error) {
	c := config{DBPath: env("DB_PATH", "/data/easydist.db"), DeployDir: env("DEPLOY_DIR", "/deploy"), WebDir: env("WEB_DIR", "/app/web"), AdminOrigin: strings.TrimRight(os.Getenv("ADMIN_ORIGIN"), "/"), BaseURL: strings.TrimRight(os.Getenv("BASE_URL"), "/"), Listen: env("LISTEN", ":8080")}
	admin, err := url.Parse(c.AdminOrigin)
	if err != nil || admin.Host == "" || (admin.Scheme != "https" && admin.Scheme != "http") || admin.Path != "" || admin.RawQuery != "" || admin.Fragment != "" || admin.User != nil {
		return c, errors.New("ADMIN_ORIGIN must be an http(s) origin without a path")
	}
	game, err := url.Parse(c.BaseURL)
	if err != nil || game.Host == "" || (game.Scheme != "https" && game.Scheme != "http") || game.Path != "" || game.RawQuery != "" || game.Fragment != "" || game.User != nil || strings.EqualFold(game.Host, admin.Host) {
		return c, errors.New("BASE_URL must be a different http(s) origin without a path")
	}
	if strings.EqualFold(game.Hostname(), admin.Hostname()) && !(admin.Scheme == "http" && game.Scheme == "http" && (admin.Hostname() == "localhost" || net.ParseIP(admin.Hostname()).IsLoopback())) {
		return c, errors.New("ADMIN_ORIGIN and BASE_URL must use different hostnames (except local HTTP development)")
	}
	for _, item := range []struct {
		key      string
		fallback int64
		out      *int64
	}{{"MAX_ZIP_BYTES", 512 << 20, &c.MaxZIP}, {"MAX_EXTRACT_BYTES", 2 << 30, &c.MaxExtract}} {
		v, err := strconv.ParseInt(env(item.key, strconv.FormatInt(item.fallback, 10)), 10, 64)
		if err != nil || v < 1 || v > 1<<40 {
			return c, fmt.Errorf("%s must be between 1 and 1099511627776", item.key)
		}
		*item.out = v
	}
	n, err := strconv.Atoi(env("MAX_ZIP_ENTRIES", "10000"))
	if err != nil || n < 1 || n > 1000000 {
		return c, errors.New("MAX_ZIP_ENTRIES must be between 1 and 1000000")
	}
	c.MaxEntries = n
	return c, nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/healthz")
		if err != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	c, err := readConfig()
	if err != nil {
		log.Fatal(err)
	}
	a, err := openApp(c)
	if err != nil {
		log.Fatal(err)
	}
	defer a.db.Close()
	if err := a.bootstrap(os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")); err != nil {
		log.Fatal(err)
	}
	if err := a.recoverDeployments(); err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: c.Listen, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Minute, WriteTimeout: 15 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			log.Print(err)
			srv.Close()
		}
	}()
	log.Printf("EasyDist listening on %s", c.Listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
