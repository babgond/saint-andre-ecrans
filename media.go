package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/image/draw"
)

const maxImage = 25 << 20
const reserve = 100 << 20

var tokenName = regexp.MustCompile("^[a-f0-9]{32}[.](png|jpg)$")
var tempName = regexp.MustCompile("^[.]upload-[0-9]+$")

type imageUpload struct {
	name, file, hash string
	width, height    int
	content, thumb   []byte
	uploadBytes      int
}

func (a *application) reserveUpload(w http.ResponseWriter) bool {
	select {
	case a.imageSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "2")
		problem(w, 503, "D’autres images sont en cours de traitement. Réessayez dans quelques secondes.")
		return false
	}
}

func readImage(w http.ResponseWriter, r *http.Request, thumbnail bool) (imageUpload, bool) {
	var item imageUpload
	r.Body = http.MaxBytesReader(w, r.Body, maxImage+(64<<10))
	name, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition"))
	if err != nil || name != "attachment" {
		problem(w, 400, "Le nom du fichier est requis.")
		return item, false
	}
	item.name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(params["filename"], "\\", "/")))
	if item.name == "" || item.name == "." || len([]rune(item.name)) > 255 {
		problem(w, 400, "Le nom du fichier est invalide.")
		return item, false
	}
	extension := strings.ToLower(filepath.Ext(item.name))
	if extension != ".png" && extension != ".jpg" && extension != ".jpeg" {
		problem(w, 415, "Choisissez un fichier PNG ou JPEG.")
		return item, false
	}
	item.content, err = io.ReadAll(r.Body)
	if err != nil || len(item.content) > maxImage {
		problem(w, 413, "Le fichier doit peser au maximum 25 Mio.")
		return item, false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(item.content))
	if err != nil || (format == "png" && extension != ".png") || (format == "jpeg" && extension == ".png") || (format != "png" && format != "jpeg") {
		problem(w, 415, "Le contenu du fichier ne correspond pas à une image PNG ou JPEG.")
		return item, false
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 10000 || cfg.Height > 10000 || int64(cfg.Width)*int64(cfg.Height) > 24_000_000 {
		problem(w, 400, "L’image dépasse 24 mégapixels ou 10 000 pixels sur un côté.")
		return item, false
	}
	img, _, err := image.Decode(bytes.NewReader(item.content))
	if err != nil {
		problem(w, 415, "L’image est endommagée.")
		return item, false
	}
	item.width, item.height = cfg.Width, cfg.Height
	if extension == ".jpeg" {
		extension = ".jpg"
	}
	item.file = hex.EncodeToString(randomBytes(16)) + extension
	digest := sha256.Sum256(item.content)
	item.hash = hex.EncodeToString(digest[:])
	item.uploadBytes = len(item.content)
	item.content = optimizeImage(item.content, img, format)
	if thumbnail {
		width, height := item.width, item.height
		scale := max(float64(width)/640, float64(height)/360)
		if scale > 1 {
			width, height = max(1, int(float64(width)/scale)), max(1, int(float64(height)/scale))
		}
		out := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.CatmullRom.Scale(out, out.Bounds(), img, img.Bounds(), draw.Src, nil)
		var result bytes.Buffer
		if err := jpeg.Encode(&result, out, &jpeg.Options{Quality: 85}); err != nil {
			internal(w, err)
			return item, false
		}
		item.thumb = result.Bytes()
	}
	return item, true
}

func thumbName(name string) string { return strings.TrimSuffix(name, filepath.Ext(name)) + ".jpg" }

func writeAtomic(dir, name string, body []byte) error {
	f, err := os.CreateTemp(dir, ".upload-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (a *application) canStore(w http.ResponseWriter, n int64) bool {
	free, _, err := space(a.data)
	if err != nil {
		internal(w, err)
		return false
	}
	if free < n+reserve {
		problem(w, 507, "Espace disque insuffisant. Libérez de la place avant de continuer.")
		return false
	}
	return true
}

func (a *application) saveImage(item imageUpload) error {
	if err := writeAtomic(filepath.Join(a.data, originals), item.file, item.content); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(a.data, thumbs), thumbName(item.file), item.thumb)
}

func (a *application) clearOrphans() {
	if err := a.cleanOrphans(); err != nil {
		log.Printf("Nettoyage des fichiers : %v", err)
	}
}

func (a *application) duplicate(w http.ResponseWriter, hash string, exclude int64) bool {
	var deleted sql.NullInt64
	err := a.db.QueryRow("SELECT deleted_at FROM publications WHERE hash=? AND id<>?", hash, exclude).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		internal(w, err)
		return true
	}
	msg := "Ce fichier est déjà dans la bibliothèque."
	if deleted.Valid {
		msg = "Ce fichier est dans la corbeille. Vous pouvez le restaurer."
	}
	problem(w, 409, msg)
	return true
}

func (a *application) upload(w http.ResponseWriter, r *http.Request) {
	draft := r.URL.Query().Get("draft") == "1"
	s := schedule{Start: r.URL.Query().Get("start"), End: r.URL.Query().Get("end"), Enabled: true, Draft: draft}
	begin, end, err := s.dates()
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	if !a.reserveUpload(w) {
		return
	}
	defer func() { <-a.imageSlots }()
	item, ok := readImage(w, r, true)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.duplicate(w, item.hash, 0) || !a.canStore(w, int64(len(item.content)+len(item.thumb))) {
		return
	}
	if err := a.saveImage(item); err != nil {
		a.clearOrphans()
		internal(w, err)
		return
	}
	result, err := a.db.Exec(`INSERT INTO publications(name,file,hash,width,height,bytes,thumb_bytes,position,start_at,end_at,enabled,draft)
 VALUES (?,?,?,?,?,?,?,(SELECT coalesce(max(position),0)+1 FROM publications),?,?,1,?)`,
		item.name, item.file, item.hash, item.width, item.height, len(item.content), len(item.thumb), begin, end, draft)
	if err != nil {
		a.clearOrphans()
		internal(w, err)
		return
	}
	id, _ := result.LastInsertId()
	jsonResponse(w, 201, map[string]any{"id": id, "width": item.width, "height": item.height,
		"bytes": len(item.content), "uploadBytes": item.uploadBytes})
}

func (a *application) replace(w http.ResponseWriter, r *http.Request) {
	if !a.reserveUpload(w) {
		return
	}
	defer func() { <-a.imageSlots }()
	item, ok := readImage(w, r, true)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var id int64
	var hash string
	var savedBytes int
	err := a.db.QueryRow("SELECT id,hash,bytes FROM publications WHERE id=? AND deleted_at IS NULL", r.PathValue("id")).Scan(&id, &hash, &savedBytes)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "Visuel introuvable.")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if hash == item.hash && savedBytes <= len(item.content) {
		w.WriteHeader(204)
		return
	}
	if a.duplicate(w, item.hash, id) || !a.canStore(w, int64(len(item.content)+len(item.thumb))) {
		return
	}
	if err := a.saveImage(item); err != nil {
		a.clearOrphans()
		internal(w, err)
		return
	}
	result, err := a.db.Exec("UPDATE publications SET name=?,file=?,hash=?,width=?,height=?,bytes=?,thumb_bytes=? WHERE id=?",
		item.name, item.file, item.hash, item.width, item.height, len(item.content), len(item.thumb), id)
	a.clearOrphans()
	changed(w, result, err)
}

func (a *application) logo(w http.ResponseWriter, r *http.Request) {
	if !a.reserveUpload(w) {
		return
	}
	defer func() { <-a.imageSlots }()
	item, ok := readImage(w, r, false)
	if !ok {
		return
	}
	if !strings.HasSuffix(item.file, ".png") {
		problem(w, 415, "Choisissez un logo PNG.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.canStore(w, int64(len(item.content))) {
		return
	}
	if err := writeAtomic(filepath.Join(a.data, branding), item.file, item.content); err != nil {
		a.clearOrphans()
		internal(w, err)
		return
	}
	_, err := a.db.Exec("UPDATE settings SET logo=? WHERE id=1", item.file)
	a.clearOrphans()
	if err != nil {
		internal(w, err)
		return
	}
	w.WriteHeader(204)
}

func (a *application) removeLogo(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.db.Exec("UPDATE settings SET logo='' WHERE id=1"); err != nil {
		internal(w, err)
		return
	}
	a.clearOrphans()
	w.WriteHeader(204)
}

func (a *application) imageFile(dir, name string) (*os.File, error) {
	return os.Open(filepath.Join(a.data, dir, name))
}

func serveImage(w http.ResponseWriter, r *http.Request, file *os.File, name string) {
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		internal(w, err)
		return
	}
	ct := "image/jpeg"
	if strings.HasSuffix(name, ".png") {
		ct = "image/png"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("ETag", `"`+name+`"`)
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (a *application) publicMedia(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !tokenName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	a.mu.RLock()
	dir := originals
	s, err := a.settings()
	if err == nil && s.Logo == name {
		dir = branding
	} else if err == nil {
		now := time.Now().Unix()
		var exists int
		err = a.db.QueryRow("SELECT 1 FROM publications WHERE file=? AND deleted_at IS NULL AND draft=0 AND enabled=1 AND (start_at IS NULL OR start_at<=?) AND (end_at IS NULL OR end_at>?)", name, now, now).Scan(&exists)
	}
	var f *os.File
	if err == nil {
		f, err = a.imageFile(dir, name)
	}
	a.mu.RUnlock()
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, no-cache")
	serveImage(w, r, f, name)
}

func (a *application) adminMedia(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !tokenName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	dir := originals
	if r.URL.Query().Get("mini") == "1" {
		dir, name = thumbs, thumbName(name)
	}
	a.mu.RLock()
	f, err := a.imageFile(dir, name)
	a.mu.RUnlock()
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	serveImage(w, r, f, name)
}
