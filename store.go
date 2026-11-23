package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const (
	originals    = "images/originals"
	thumbs       = "images/thumbnails"
	branding     = "branding"
	databasePath = "database/ecrans.db"
	backups      = "backups"
	dateLayout   = "2006-01-02T15:04"
)

var paris, _ = time.LoadLocation("Europe/Paris")

type application struct {
	db         *sql.DB
	data       string
	mu         sync.RWMutex
	backupMu   sync.Mutex
	imageSlots chan struct{}
	keep       int
	auth       authState
}

var schema = `
CREATE TABLE IF NOT EXISTS publications (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 name TEXT NOT NULL, file TEXT NOT NULL UNIQUE, hash TEXT NOT NULL UNIQUE,
 width INTEGER NOT NULL, height INTEGER NOT NULL, bytes INTEGER NOT NULL, thumb_bytes INTEGER NOT NULL,
 position INTEGER NOT NULL, start_at INTEGER, end_at INTEGER,
 enabled INTEGER NOT NULL, draft INTEGER NOT NULL, deleted_at INTEGER
);
CREATE INDEX IF NOT EXISTS publications_order ON publications(position,id);
CREATE TABLE IF NOT EXISTS settings (
 id INTEGER PRIMARY KEY CHECK(id=1), duration INTEGER NOT NULL, transition TEXT NOT NULL,
 transition_ms INTEGER NOT NULL, fallback INTEGER NOT NULL, logo TEXT NOT NULL
);
INSERT OR IGNORE INTO settings VALUES (1, 15, 'fade', 1000, 0, '');
CREATE TABLE IF NOT EXISTS display_identity (
 id INTEGER PRIMARY KEY CHECK(id=1), name TEXT NOT NULL
);
`

func openApp(root string) (*application, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, part := range []string{"database", originals, thumbs, branding, backups} {
		if err := os.MkdirAll(filepath.Join(root, part), 0700); err != nil {
			return nil, err
		}
	}
	dbURL := url.URL{Scheme: "file", Path: filepath.Join(root, databasePath), RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"}
	db, err := sql.Open("sqlite", dbURL.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &application{db: db, data: root, keep: 7, imageSlots: make(chan struct{}, 2)}, nil
}

type publication struct {
	ID      int64         `json:"id"`
	Name    string        `json:"name"`
	File    string        `json:"file"`
	Width   int           `json:"width"`
	Height  int           `json:"height"`
	Bytes   int64         `json:"bytes"`
	Start   string        `json:"start"`
	End     string        `json:"end"`
	Enabled bool          `json:"enabled"`
	Draft   bool          `json:"draft"`
	Status  string        `json:"status"`
	Deleted sql.NullInt64 `json:"-"`
}

func statusAt(now int64, start, end, deleted sql.NullInt64, enabled, draft bool) string {
	switch {
	case deleted.Valid:
		return "trash"
	case draft:
		return "draft"
	case !enabled:
		return "disabled"
	case end.Valid && end.Int64 <= now:
		return "expired"
	case start.Valid && start.Int64 > now:
		return "scheduled"
	default:
		return "active"
	}
}

func dateText(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	return time.Unix(value.Int64, 0).In(paris).Format(dateLayout)
}

func parisDate(raw string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}
	wall, err := time.Parse(dateLayout, raw)
	if err != nil || wall.Year() < 1900 || wall.Year() > 9999 {
		return nil, errors.New("Date invalide.")
	}
	// Test both offsets around a change of civil time. Prefer the first occurrence of a repeated hour.
	var match *int64
	for _, day := range []int{-1, 1} {
		_, offset := wall.AddDate(0, 0, day).In(paris).Zone()
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if candidate.In(paris).Format(dateLayout) == raw && (match == nil || candidate.Unix() < *match) {
			v := candidate.Unix()
			match = &v
		}
	}
	if match == nil {
		return nil, errors.New("Cette heure locale n’existe pas en Europe/Paris.")
	}
	return match, nil
}

type schedule struct {
	Start   string `json:"start"`
	End     string `json:"end"`
	Enabled bool   `json:"enabled"`
	Draft   bool   `json:"draft"`
}

func (s schedule) dates() (*int64, *int64, error) {
	begin, err := parisDate(s.Start)
	if err != nil {
		return nil, nil, err
	}
	end, err := parisDate(s.End)
	if err == nil && begin != nil && end != nil && *begin >= *end {
		err = errors.New("La fin doit être postérieure au début.")
	}
	return begin, end, err
}

func (a *application) publications(now time.Time) ([]publication, error) {
	rows, err := a.db.Query("SELECT id,name,file,width,height,bytes,start_at,end_at,enabled,draft,deleted_at FROM publications ORDER BY position,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []publication{}
	for rows.Next() {
		var p publication
		var start, end sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Name, &p.File, &p.Width, &p.Height, &p.Bytes, &start, &end, &p.Enabled, &p.Draft, &p.Deleted); err != nil {
			return nil, err
		}
		p.Start, p.End = dateText(start), dateText(end)
		p.Status = statusAt(now.Unix(), start, end, p.Deleted, p.Enabled, p.Draft)
		result = append(result, p)
	}
	return result, rows.Err()
}

type settings struct {
	Duration     int    `json:"duration"`
	Transition   string `json:"transition"`
	TransitionMS int    `json:"transitionMs"`
	Fallback     bool   `json:"fallback"`
	Logo         string `json:"logo"`
}

func (a *application) settings() (settings, error) {
	var s settings
	err := a.db.QueryRow("SELECT duration,transition,transition_ms,fallback,logo FROM settings WHERE id=1").Scan(&s.Duration, &s.Transition, &s.TransitionMS, &s.Fallback, &s.Logo)
	return s, err
}

const defaultDisplayName = "Saint André"

func (a *application) displayName() (string, error) {
	var name string
	err := a.db.QueryRow("SELECT coalesce((SELECT name FROM display_identity WHERE id=1), ?)", defaultDisplayName).Scan(&name)
	return name, err
}

func (a *application) saveDisplayName(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) {
		problem(w, 400, "Saisissez un nom de 1 à 80 caractères sur une seule ligne.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.db.Exec("INSERT INTO display_identity (id,name) VALUES (1,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name", name); err != nil {
		internal(w, err)
		return
	}
	w.WriteHeader(204)
}

func (a *application) saveSettings(w http.ResponseWriter, r *http.Request) {
	var s settings
	if !decode(w, r, &s) {
		return
	}
	if s.Duration < 5 || s.Duration > 120 || s.TransitionMS < 500 || s.TransitionMS > 3000 ||
		!slices.Contains([]string{"fade", "zoom", "slide", "black", "none"}, s.Transition) {
		problem(w, 400, "Les réglages de diffusion sont invalides.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.db.Exec("UPDATE settings SET duration=?,transition=?,transition_ms=?,fallback=? WHERE id=1", s.Duration, s.Transition, s.TransitionMS, s.Fallback); err != nil {
		internal(w, err)
		return
	}
	w.WriteHeader(204)
}

func space(root string) (int64, int64, error) {
	var info syscall.Statfs_t
	if err := syscall.Statfs(root, &info); err != nil {
		return 0, 0, err
	}
	return int64(info.Bavail) * info.Bsize, int64(info.Blocks) * info.Bsize, nil
}

func (a *application) state(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	now := time.Now()
	images, err := a.publications(now)
	if err != nil {
		internal(w, err)
		return
	}
	s, err := a.settings()
	if err != nil {
		internal(w, err)
		return
	}
	name, err := a.displayName()
	if err != nil {
		internal(w, err)
		return
	}
	used, err := folderSize(a.data)
	if err != nil {
		internal(w, err)
		return
	}
	free, total, err := space(a.data)
	if err != nil {
		internal(w, err)
		return
	}
	archives, err := a.backupList()
	if err != nil {
		internal(w, err)
		return
	}
	jsonResponse(w, 200, struct {
		DisplayName string        `json:"displayName"`
		Images      []publication `json:"images"`
		Settings    settings      `json:"settings"`
		Used        int64         `json:"used"`
		Free        int64         `json:"free"`
		Total       int64         `json:"total"`
		Today       string        `json:"today"`
		Backups     []backupFile  `json:"backups"`
		Account     string        `json:"account"`
	}{name, images, s, used, free, total, now.In(paris).Format("2006-01-02"), archives, a.session(r).Name})
}

func folderSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func changed(w http.ResponseWriter, result sql.Result, err error) {
	if err != nil {
		internal(w, err)
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		internal(w, err)
		return
	}
	if n == 0 {
		problem(w, 404, "Visuel introuvable.")
		return
	}
	w.WriteHeader(204)
}

func (a *application) edit(w http.ResponseWriter, r *http.Request) {
	var s schedule
	if !decode(w, r, &s) {
		return
	}
	begin, end, err := s.dates()
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	result, err := a.db.Exec("UPDATE publications SET start_at=?,end_at=?,enabled=?,draft=? WHERE id=? AND deleted_at IS NULL", begin, end, s.Enabled, s.Draft, r.PathValue("id"))
	changed(w, result, err)
}

func (a *application) reorder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []int64 `json:"ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	images, err := a.publications(time.Now())
	if err != nil {
		internal(w, err)
		return
	}
	active := make(map[int64]bool)
	var deleted []int64
	for _, p := range images {
		if p.Deleted.Valid {
			deleted = append(deleted, p.ID)
		} else {
			active[p.ID] = true
		}
	}
	if len(active) != len(input.IDs) {
		problem(w, 409, "La liste a changé. Actualisez la bibliothèque.")
		return
	}
	for _, id := range input.IDs {
		if !active[id] {
			problem(w, 409, "La liste a changé. Actualisez la bibliothèque.")
			return
		}
		delete(active, id)
	}
	tx, err := a.db.Begin()
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	for i, id := range append(input.IDs, deleted...) {
		if _, err := tx.Exec("UPDATE publications SET position=? WHERE id=?", i+1, id); err != nil {
			internal(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	w.WriteHeader(204)
}

func (a *application) trash(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.IDs) == 0 || len(input.IDs) > 1000 {
		problem(w, 400, "Sélectionnez entre 1 et 1 000 visuels.")
		return
	}
	var query string
	switch input.Action {
	case "trash":
		query = "UPDATE publications SET deleted_at=strftime('%s','now') WHERE id=? AND deleted_at IS NULL"
	case "restore":
		query = "UPDATE publications SET deleted_at=NULL, position=(SELECT coalesce(max(position),0)+1 FROM publications) WHERE id=? AND deleted_at IS NOT NULL"
	case "purge":
		query = "DELETE FROM publications WHERE id=? AND deleted_at IS NOT NULL"
	default:
		problem(w, 400, "Action invalide.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	tx, err := a.db.Begin()
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	for _, id := range input.IDs {
		result, err := tx.Exec(query, id)
		if err != nil {
			internal(w, err)
			return
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			problem(w, 409, "La sélection a changé. Actualisez la bibliothèque.")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	if input.Action == "purge" {
		a.clearOrphans()
	}
	w.WriteHeader(204)
}

type slide struct {
	URL string `json:"url"`
	ID  int64  `json:"id"`
}

func (a *application) playlist(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	now := time.Now().Unix()
	s, err := a.settings()
	if err != nil {
		internal(w, err)
		return
	}
	name, err := a.displayName()
	if err != nil {
		internal(w, err)
		return
	}
	rows, err := a.db.Query("SELECT id,file FROM publications WHERE deleted_at IS NULL AND draft=0 AND enabled=1 AND (start_at IS NULL OR start_at<=?) AND (end_at IS NULL OR end_at>?) ORDER BY position,id", now, now)
	if err != nil {
		internal(w, err)
		return
	}
	defer rows.Close()
	items := []slide{}
	for rows.Next() {
		var item slide
		if err := rows.Scan(&item.ID, &item.URL); err != nil {
			internal(w, err)
			return
		}
		item.URL = "/media/" + item.URL
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		internal(w, err)
		return
	}
	if s.Logo != "" {
		s.Logo = "/media/" + s.Logo
	}
	jsonResponse(w, 200, struct {
		DisplayName string   `json:"displayName"`
		Slides      []slide  `json:"slides"`
		Settings    settings `json:"settings"`
	}{name, items, s})
}

func (a *application) preview(w http.ResponseWriter, r *http.Request) {
	a.page("screen.html")(w, r)
}

func matchDiskFile(dir, name string) bool {
	if dir == thumbs {
		return strings.HasSuffix(name, ".jpg") && tokenName.MatchString(name)
	}
	return tokenName.MatchString(name)
}
