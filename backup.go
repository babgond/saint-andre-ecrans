package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var archiveName = regexp.MustCompile("^ecrans-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{8}[.]tar[.]gz$")
var snapshotName = regexp.MustCompile("^[.]snapshot-[0-9]+$")
var partialBackup = regexp.MustCompile("^[.]archive-[0-9]+$")

type backupFile struct {
	Name  string    `json:"name"`
	Bytes int64     `json:"bytes"`
	At    time.Time `json:"at"`
}

func backupRetention() int {
	n, err := strconv.Atoi(os.Getenv("BACKUP_KEEP"))
	if err != nil || n < 1 || n > 365 {
		return 7
	}
	return n
}

func (a *application) cleanOrphans() error {
	rows, err := a.db.Query("SELECT file FROM publications")
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		keep[storagePath(a.data, originals, name)] = true
		keep[storagePath(a.data, thumbs, thumbName(name))] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	s, err := a.settings()
	if err != nil {
		return err
	}
	if s.Logo != "" {
		keep[storagePath(a.data, branding, s.Logo)] = true
	}
	var failures []error
	for _, dir := range []string{originals, thumbs, branding} {
		ps, err := os.ReadDir(filepath.Join(a.data, dir))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, p := range ps {
			name := p.Name()
			path := storagePath(a.data, dir, name)
			if p.Type().IsRegular() && !keep[path] && (matchDiskFile(dir, name) || tempName.MatchString(name)) {
				if err := os.Remove(path); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}
	return errors.Join(failures...)
}

func (a *application) backupList() ([]backupFile, error) {
	items, err := os.ReadDir(filepath.Join(a.data, backups))
	if err != nil {
		return nil, err
	}
	result := []backupFile{}
	for _, item := range items {
		if !item.Type().IsRegular() || !archiveName.MatchString(item.Name()) {
			continue
		}
		info, err := item.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		at, _ := time.Parse("20060102T150405Z", item.Name()[7:23])
		result = append(result, backupFile{item.Name(), info.Size(), at})
	}
	slices.SortFunc(result, func(a, b backupFile) int { return strings.Compare(b.Name, a.Name) })
	return result, nil
}

func (a *application) maintenance(ctx context.Context) {
	run := func() {
		a.mu.Lock()
		a.clearOrphans()
		a.mu.Unlock()
		a.backupMu.Lock()
		defer a.backupMu.Unlock()
		dir := filepath.Join(a.data, backups)
		if items, err := os.ReadDir(dir); err == nil {
			for _, p := range items {
				if p.Type().IsRegular() && (snapshotName.MatchString(p.Name()) || partialBackup.MatchString(p.Name())) {
					_ = os.Remove(filepath.Join(dir, p.Name()))
				}
			}
		}
		list, err := a.backupList()
		if err == nil && (len(list) == 0 || list[0].At.In(paris).Format(time.DateOnly) != time.Now().In(paris).Format(time.DateOnly)) {
			_, err = a.createBackup()
		}
		if err != nil {
			log.Printf("Sauvegarde automatique : %v", err)
		}
	}
	run()
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (a *application) makeBackup(w http.ResponseWriter, r *http.Request) {
	if !a.backupMu.TryLock() {
		problem(w, 409, "Une sauvegarde est déjà en cours.")
		return
	}
	defer a.backupMu.Unlock()
	b, err := a.createBackup()
	if err != nil {
		if errors.Is(err, errBackupSpace) {
			problem(w, 507, err.Error())
			return
		}
		internal(w, err)
		return
	}
	jsonResponse(w, 201, b)
}

var errBackupSpace = errors.New("Espace disque insuffisant pour créer une sauvegarde.")

type snapshotFile struct {
	file *os.File
	name string
	size int64
}

func (a *application) snapshot(dir string) ([]snapshotFile, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	files := []snapshotFile{}
	fail := func(err error) ([]snapshotFile, error) {
		for _, f := range files {
			f.file.Close()
		}
		return nil, err
	}
	add := func(relative, path string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		files = append(files, snapshotFile{f, relative, info.Size()})
		return nil
	}
	images, err := a.publications(time.Now())
	if err != nil {
		return fail(err)
	}
	s, err := a.settings()
	if err != nil {
		return fail(err)
	}
	for _, p := range images {
		for _, name := range []string{filepath.Join(originals, p.File), filepath.Join(thumbs, thumbName(p.File))} {
			if err := add(name, filepath.Join(a.data, name)); err != nil {
				return fail(err)
			}
		}
	}
	if s.Logo != "" {
		name := filepath.Join(branding, s.Logo)
		if err := add(name, filepath.Join(a.data, name)); err != nil {
			return fail(err)
		}
	}
	free, _, err := space(a.data)
	if err != nil {
		return fail(err)
	}
	var needed int64 = reserve
	for _, f := range files {
		needed += f.size + 8192
	}
	dbInfo, err := os.Stat(filepath.Join(a.data, databasePath))
	if err != nil {
		return fail(err)
	}
	needed += 3 * dbInfo.Size()
	wal, err := os.Stat(filepath.Join(a.data, databasePath) + "-wal")
	if err == nil {
		needed += 3 * wal.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if free < needed {
		return fail(errBackupSpace)
	}
	f, err := os.CreateTemp(dir, ".snapshot-")
	if err != nil {
		return fail(err)
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if _, err := a.db.Exec("VACUUM INTO ?", path); err != nil {
		return fail(err)
	}
	if err := add(databasePath, path); err != nil {
		return fail(err)
	}
	return files, nil
}

func (a *application) createBackup() (backupFile, error) {
	dir := filepath.Join(a.data, backups)
	files, err := a.snapshot(dir)
	if err != nil {
		return backupFile{}, err
	}
	defer func() {
		for _, f := range files {
			f.file.Close()
		}
	}()
	f, err := os.CreateTemp(dir, ".archive-")
	if err != nil {
		return backupFile{}, err
	}
	defer os.Remove(f.Name())
	if err := archive(f, files); err != nil {
		f.Close()
		return backupFile{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return backupFile{}, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return backupFile{}, err
	}
	if err := f.Close(); err != nil {
		return backupFile{}, err
	}
	now := time.Now().UTC()
	name := fmt.Sprintf("ecrans-%s-%x.tar.gz", now.Format("20060102T150405Z"), randomBytes(4))
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return backupFile{}, err
	}
	if err := syncDir(dir); err != nil {
		return backupFile{}, err
	}
	list, err := a.backupList()
	if err == nil && len(list) > a.keep {
		// Keep this new archive even when two manual backups finish during the same second.
		saved := 1
		for _, b := range list {
			if b.Name == name {
				continue
			}
			if saved < a.keep {
				saved++
				continue
			}
			if err := os.Remove(filepath.Join(dir, b.Name)); err != nil {
				log.Printf("Rétention des sauvegardes : %v", err)
			}
		}
	}
	return backupFile{name, info.Size(), now}, nil
}

func archive(out io.Writer, files []snapshotFile) error {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Size: f.size, Mode: 0600, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := io.CopyN(tw, f.file, f.size); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func (a *application) getBackup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !archiveName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(storagePath(a.data, backups, name))
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func restore(source, destination string) error {
	dest, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0700); err != nil {
		return err
	}
	items, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	if len(items) != 0 {
		return errors.New("La destination de restauration doit être un volume vide")
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	stage, err := os.MkdirTemp(dest, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, dir := range []string{"database", originals, thumbs, branding, backups} {
		if err := os.MkdirAll(filepath.Join(stage, dir), 0700); err != nil {
			return err
		}
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		dir, name := filepath.Split(h.Name)
		dir = strings.TrimSuffix(dir, "/")
		permitted := h.Name == databasePath || ((dir == originals || dir == thumbs || dir == branding) && matchDiskFile(dir, name))
		if !permitted || h.Typeflag != tar.TypeReg || h.Size < 1 || (h.Name != databasePath && h.Size > maxImage) {
			return fmt.Errorf("Entrée d’archive invalide : %s", h.Name)
		}
		out, err := os.OpenFile(filepath.Join(stage, h.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(out, tr)
		if err := errors.Join(writeErr, out.Sync(), out.Close()); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return err
	}
	if err := validateRestore(stage); err != nil {
		return err
	}
	var promoted []string
	for _, dir := range []string{"database", "images", "branding", "backups"} {
		if err := os.Rename(filepath.Join(stage, dir), filepath.Join(dest, dir)); err != nil {
			for _, moved := range promoted {
				_ = os.Rename(filepath.Join(dest, moved), filepath.Join(stage, moved))
			}
			return err
		}
		promoted = append(promoted, dir)
	}
	return syncDir(dest)
}

func validateRestore(stage string) error {
	path := filepath.Join(stage, databasePath)
	if _, err := os.Stat(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("La base SQLite restaurée est corrompue")
	}
	a := &application{db: db, data: stage}
	images, err := a.publications(time.Now())
	if err != nil {
		return err
	}
	s, err := a.settings()
	if err != nil {
		return err
	}
	check := func(dir, name string) error {
		if !matchDiskFile(dir, name) {
			return errors.New("Nom de fichier invalide dans la base restaurée")
		}
		_, err := os.Stat(storagePath(stage, dir, name))
		return err
	}
	for _, p := range images {
		if err := check(originals, p.File); err != nil {
			return err
		}
		if err := check(thumbs, thumbName(p.File)); err != nil {
			return err
		}
	}
	if s.Logo != "" {
		return check(branding, s.Logo)
	}
	return nil
}
