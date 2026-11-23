package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata"
)

//go:embed web/*
var web embed.FS

var appVersion = "dev"

var pages = template.Must(template.ParseFS(web, "web/*.html"))

func main() {
	if len(os.Args) == 4 && os.Args[1] == "restore" {
		if err := restore(os.Args[2], os.Args[3]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) != 1 {
		log.Fatal("Usage : ecrans [restore archive destination]")
	}
	data := env("DATA_DIR", "./data")
	app, err := openApp(data)
	if err != nil {
		log.Fatal(err)
	}
	defer app.db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.configureAuth(ctx); err != nil {
		log.Fatal(err)
	}
	app.keep = backupRetention()
	go app.maintenance(ctx)

	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: app.routes(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("Saint André Écrans écoute sur %s", srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (a *application) routes() http.Handler {
	mux := http.NewServeMux()
	assets, _ := fs.Sub(web, "web")
	for _, slug := range []string{"ui.css", "admin.js", "screen.css", "screen.js"} {
		mux.Handle("GET /assets/"+slug, http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	}
	mux.HandleFunc("GET /{$}", a.page("screen.html"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			http.Error(w, "Indisponible", 503)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/playlist", a.playlist)
	mux.HandleFunc("GET /media/{file}", a.publicMedia)
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("POST /auth/logout", a.protected(a.logout))
	mux.HandleFunc("GET /admin", a.adminPage)
	mux.HandleFunc("GET /admin/preview", a.protected(a.preview))
	mux.HandleFunc("GET /api/admin/state", a.protected(a.state))
	mux.HandleFunc("POST /api/admin/images", a.protected(a.upload))
	mux.HandleFunc("PATCH /api/admin/images/{id}", a.protected(a.edit))
	mux.HandleFunc("POST /api/admin/images/{id}/replace", a.protected(a.replace))
	mux.HandleFunc("POST /api/admin/order", a.protected(a.reorder))
	mux.HandleFunc("POST /api/admin/trash", a.protected(a.trash))
	mux.HandleFunc("GET /api/admin/image/{file}", a.protected(a.adminMedia))
	mux.HandleFunc("PUT /api/admin/settings", a.protected(a.saveSettings))
	mux.HandleFunc("PUT /api/admin/clock", a.protected(a.saveClock))
	mux.HandleFunc("PUT /api/admin/display-name", a.protected(a.saveDisplayName))
	mux.HandleFunc("POST /api/admin/logo", a.protected(a.logo))
	mux.HandleFunc("DELETE /api/admin/logo", a.protected(a.removeLogo))
	mux.HandleFunc("POST /api/admin/backups", a.protected(a.makeBackup))
	mux.HandleFunc("GET /api/admin/backups/{file}", a.protected(a.getBackup))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		policy := "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob:; connect-src 'self'; frame-src 'self'; form-action 'self'; base-uri 'none'"
		if r.URL.Path != "/" {
			policy += "; frame-ancestors 'self'"
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		h.Set("Content-Security-Policy", policy)
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			h.Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *application) page(templateName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, err := a.displayName()
		if err != nil {
			internal(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := struct {
			DisplayName string
			Version     string
		}{DisplayName: name, Version: appVersion}
		if err := pages.ExecuteTemplate(w, templateName, data); err != nil {
			log.Printf("Affichage de %s : %v", templateName, err)
		}
	}
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func problem(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

func internal(w http.ResponseWriter, err error) {
	log.Printf("Erreur : %v", err)
	problem(w, 500, "Une erreur interne est survenue.")
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if kind := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]); kind != "application/json" {
		problem(w, 415, "Un document JSON est attendu.")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil || d.Decode(&struct{}{}) != io.EOF {
		problem(w, 400, "Les données transmises sont invalides.")
		return false
	}
	return true
}

func validOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("PUBLIC_URL doit contenir une origine HTTPS")
	}
	u.Host = strings.ToLower(strings.TrimSuffix(u.Host, ":443"))
	return u, nil
}

func storagePath(root, dir, name string) string { return filepath.Join(root, dir, name) }
