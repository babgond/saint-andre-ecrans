package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func testApp(t *testing.T) *application {
	t.Helper()
	a, err := openApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	a.auth.origin = "https://ecrans.test"
	a.auth.sessions = map[string]adminSession{"test": {Name: "Administration de test", Expires: time.Now().Add(time.Hour)}}
	return a
}

func call(t *testing.T, a *application, method, path string, body io.Reader, authenticated bool, headers map[string]string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://ecrans.test"+path, body)
	if authenticated {
		r.AddCookie(&http.Cookie{Name: "__Host-ecrans-session", Value: "test"})
		r.Header.Set("Origin", a.auth.origin)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: statut %d, attendu %d. %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func callJSON(t *testing.T, a *application, method, path, payload string, want int) {
	t.Helper()
	call(t, a, method, "/api/admin/"+path, strings.NewReader(payload), true, map[string]string{"Content-Type": "application/json"}, want)
}

func pngImage(t *testing.T, r uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 160, 90))
	img.SetRGBA(0, 0, color.RGBA{R: r, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func postImage(t *testing.T, a *application, path, name string, image []byte, status int) {
	t.Helper()
	call(t, a, "POST", "/api/admin/"+path, bytes.NewReader(image), true, map[string]string{"Content-Disposition": "attachment; filename*=UTF-8''" + url.PathEscape(name)}, status)
}

func playlistIDs(t *testing.T, a *application, want ...int64) {
	t.Helper()
	w := call(t, a, "GET", "/api/playlist", nil, false, nil, 200)
	var p struct{ Slides []slide }
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, s := range p.Slides {
		ids = append(ids, s.ID)
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ordre %v, attendu %v", ids, want)
	}
}

func TestDisplayName(t *testing.T) {
	a := testApp(t)
	// A volume created before this setting has no display_identity table.
	if _, err := a.db.Exec("DROP TABLE display_identity"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := openApp(a.data)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.db.Close()
	upgraded.auth.origin = a.auth.origin
	upgraded.auth.sessions = a.auth.sessions

	check := func(path string, signedIn bool, expected string) {
		t.Helper()
		result := call(t, upgraded, "GET", path, nil, signedIn, nil, 200)
		if !strings.Contains(result.Body.String(), expected) {
			t.Errorf("%s : nom attendu %q", path, expected)
		}
	}
	check("/", false, "<span>Saint André</span>")
	call(t, upgraded, "PUT", "/api/admin/display-name", strings.NewReader(`{"name":"Autre site"}`), false, nil, 401)
	callJSON(t, upgraded, "PUT", "display-name", `{"name":" "}`, 400)
	callJSON(t, upgraded, "PUT", "display-name", `{"name":"Lieu\nsur deux lignes"}`, 400)
	callJSON(t, upgraded, "PUT", "display-name", `{"name":"`+strings.Repeat("é", 81)+`"}`, 400)
	callJSON(t, upgraded, "PUT", "display-name", `{"name":"  Médiathèque <Nord>  "}`, 204)
	check("/", false, "<span>Médiathèque &lt;Nord&gt;</span>")
	check("/admin", false, "<title>Connexion · Médiathèque &lt;Nord&gt; Écrans</title>")
	check("/admin", true, `id="display-name">Médiathèque &lt;Nord&gt;</span>`)
	check("/admin/preview", true, "<span>Médiathèque &lt;Nord&gt;</span>")
	for _, route := range []string{"/api/playlist", "/api/admin/state"} {
		result := call(t, upgraded, "GET", route, nil, route == "/api/admin/state", nil, 200)
		var branding struct {
			DisplayName string
		}
		if err := json.Unmarshal(result.Body.Bytes(), &branding); err != nil || branding.DisplayName != "Médiathèque <Nord>" {
			t.Errorf("%s : nom affiché %q, erreur %v", route, branding.DisplayName, err)
		}
	}
}

func TestPublicationLifecycleAndRestore(t *testing.T) {
	a := testApp(t)
	call(t, a, "GET", "/api/admin/state", nil, false, nil, 401)
	call(t, a, "GET", "/admin/preview", nil, false, nil, 303)
	call(t, a, "GET", "/admin", nil, false, nil, 200)
	call(t, a, "GET", "/assets/admin.js", nil, false, nil, 200)
	call(t, a, "POST", "/api/admin/backups", nil, true, map[string]string{"Origin": "https://ailleurs.test"}, 403)
	playlistIDs(t, a)

	one := pngImage(t, 1)
	postImage(t, a, "images", "Portes ouvertes.png", one, 201)
	postImage(t, a, "images", "duplicata.png", one, 409)
	postImage(t, a, "images", "faux.png", []byte("pas une image"), 415)
	postImage(t, a, "images?start=2099-06-01T08%3A00", "Été.png", pngImage(t, 2), 201)
	postImage(t, a, "images?draft=1", "Brouillon.png", pngImage(t, 3), 201)
	postImage(t, a, "images?end=2020-01-01T10%3A00", "Expirée.png", pngImage(t, 4), 201)
	playlistIDs(t, a, 1)
	images, err := a.publications(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 4 || images[1].Name != "Été.png" || images[1].Status != "scheduled" || images[2].Status != "draft" || images[3].Status != "expired" {
		t.Fatalf("bibliothèque invalide : %+v", images)
	}
	old := "/media/" + images[0].File
	w := call(t, a, "GET", old, nil, false, nil, 200)
	if !bytes.Equal(w.Body.Bytes(), one) {
		t.Fatal("l’original a changé")
	}
	call(t, a, "GET", "/media/"+images[1].File, nil, false, nil, 404)
	call(t, a, "GET", "/api/admin/image/"+images[0].File+"?mini=1", nil, true, nil, 200)
	if _, err := a.db.Exec("UPDATE publications SET start_at=? WHERE id=2", time.Now().Unix()-1); err != nil {
		t.Fatal(err)
	}
	playlistIDs(t, a, 1, 2)
	callJSON(t, a, "PATCH", "images/4", `{"start":"","end":"","enabled":true,"draft":false}`, 204)
	callJSON(t, a, "POST", "order", `{"ids":[4,2,1,3]}`, 204)
	playlistIDs(t, a, 4, 2, 1)
	postImage(t, a, "images/1/replace", "Nouveau.png", pngImage(t, 5), 204)
	call(t, a, "GET", old, nil, false, nil, 404)
	playlistIDs(t, a, 4, 2, 1)
	callJSON(t, a, "POST", "trash", `{"action":"trash","ids":[4,2]}`, 204)
	playlistIDs(t, a, 1)
	callJSON(t, a, "POST", "trash", `{"action":"restore","ids":[2]}`, 204)
	playlistIDs(t, a, 1, 2)
	callJSON(t, a, "POST", "trash", `{"action":"purge","ids":[4]}`, 204)
	if _, err := os.Stat(filepath.Join(a.data, originals, images[3].File)); !os.IsNotExist(err) {
		t.Fatal("original à supprimer retrouvé")
	}
	postImage(t, a, "logo", "Logo.png", pngImage(t, 6), 204)
	callJSON(t, a, "PUT", "settings", `{"duration":6,"transition":"slide","transitionMs":500,"fallback":true}`, 204)
	callJSON(t, a, "PUT", "display-name", `{"name":"Médiathèque"}`, 204)
	w = call(t, a, "POST", "/api/admin/backups", nil, true, nil, 201)
	var b backupFile
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	call(t, a, "GET", "/api/admin/backups/"+b.Name, nil, true, nil, 200)
	path := filepath.Join(a.data, backups, b.Name)
	dest := t.TempDir()
	if err := restore(path, dest); err != nil {
		t.Fatal(err)
	}
	if err := restore(path, dest); err == nil {
		t.Fatal("restauration sur un volume non vide acceptée")
	}
	restored, err := openApp(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.db.Close()
	s, err := restored.settings()
	if err != nil || s.Logo == "" || s.Duration != 6 || !s.Fallback {
		t.Fatalf("paramètres restaurés : %+v, %v", s, err)
	}
	if _, err := os.Stat(storagePath(dest, branding, s.Logo)); err != nil {
		t.Fatal(err)
	}
	if name, err := restored.displayName(); err != nil || name != "Médiathèque" {
		t.Fatalf("nom restauré : %q, %v", name, err)
	}
	playlistIDs(t, restored, 1, 2)
	dest2 := t.TempDir()
	archiveBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(truncated, archiveBytes[:len(archiveBytes)-5], 0600); err != nil {
		t.Fatal(err)
	}
	if err := restore(truncated, dest2); err == nil {
		t.Fatal("archive tronquée acceptée")
	}
	files, err := os.ReadDir(dest2)
	if err != nil || len(files) != 0 {
		t.Fatalf("restauration incomplète : %v %v", files, err)
	}
}

func TestParisScheduling(t *testing.T) {
	if _, err := parisDate("2026-03-29T02:30"); err == nil {
		t.Fatal("heure inexistante acceptée")
	}
	got, err := parisDate("2026-10-25T02:30")
	want := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).Unix()
	if err != nil || *got != want {
		t.Fatalf("première occurrence attendue, erreur %v", err)
	}
	if _, _, err := (schedule{Start: "2026-03-28T10:00", End: "2026-03-28T09:00"}).dates(); err == nil {
		t.Fatal("période inversée acceptée")
	}
}

func TestOIDC(t *testing.T) {
	a := testApp(t)
	a.auth.attempts = map[string]loginAttempt{}
	a.auth.allowed = map[string]bool{"admin": true}
	a.auth.tenant = "tenant"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	var challenge string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/keys" {
			jsonResponse(w, 200, map[string]any{"keys": []any{map[string]string{
				"kty": "RSA", "alg": "RS256", "kid": "test",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": "AQAB",
			}}})
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			http.Error(w, "form", 400)
			return
		}
		digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
			t.Error("PKCE invalide")
			http.Error(w, "pkce", 400)
			return
		}
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test"}`))
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Error(err)
			return
		}
		signed := header + "." + base64.RawURLEncoding.EncodeToString(payload)
		hash := sha256.Sum256([]byte(signed))
		signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
		if err != nil {
			t.Error(err)
			return
		}
		jsonResponse(w, 200, map[string]string{"access_token": "inutile", "token_type": "Bearer", "id_token": signed + "." + base64.RawURLEncoding.EncodeToString(signature)})
	}))
	defer tokenServer.Close()
	a.auth.oauth = oauth2.Config{ClientID: "client", ClientSecret: "secret", RedirectURL: "https://ecrans.test/auth/callback",
		Endpoint: oauth2.Endpoint{AuthURL: "https://example.test/authorize", TokenURL: tokenServer.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}
	a.auth.verifier = oidc.NewVerifier(tokenServer.URL, oidc.NewRemoteKeySet(context.Background(), tokenServer.URL+"/keys"), &oidc.Config{ClientID: "client"})
	handler := a.routes()
	try := func(change map[string]string, success bool) {
		t.Helper()
		w := call(t, a, "GET", "/auth/login", nil, false, nil, 303)
		redirect, err := url.Parse(w.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		query := redirect.Query()
		challenge = query.Get("code_challenge")
		if query.Get("code_challenge_method") != "S256" || challenge == "" {
			t.Fatal("PKCE absent")
		}
		claims = map[string]any{
			"iss": tokenServer.URL, "aud": "client", "tid": "tenant", "oid": "admin", "name": "Administratrice",
			"exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": query.Get("nonce"),
		}
		for k, v := range change {
			claims[k] = v
		}
		r := httptest.NewRequest("GET", "/auth/callback?state="+query.Get("state")+"&code=test", nil)
		r.AddCookie(w.Result().Cookies()[0])
		callback := httptest.NewRecorder()
		handler.ServeHTTP(callback, r)
		expected := "/admin?connexion=refusee"
		if success {
			expected = "/admin"
		}
		if callback.Code != 303 || callback.Header().Get("Location") != expected {
			t.Fatalf("callback : %d %s", callback.Code, callback.Header().Get("Location"))
		}
		if success {
			sessionCookies := callback.Result().Cookies()
			c := sessionCookies[len(sessionCookies)-1]
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("cookie insuffisamment sécurisé")
			}
			stateReq := httptest.NewRequest("GET", "/api/admin/state", nil)
			stateReq.AddCookie(c)
			state := httptest.NewRecorder()
			handler.ServeHTTP(state, stateReq)
			if state.Code != 200 || !strings.Contains(state.Body.String(), "Administratrice") {
				t.Fatal("session absente")
			}
		}
		replay := httptest.NewRecorder()
		handler.ServeHTTP(replay, r)
		if replay.Header().Get("Location") != "/admin?connexion=refusee" {
			t.Fatal("réutilisation de state acceptée")
		}
	}
	try(nil, true)
	try(map[string]string{"tid": "autre"}, false)
	try(map[string]string{"oid": "autre"}, false)
	try(map[string]string{"nonce": "autre"}, false)
	try(map[string]string{"aud": "autre"}, false)
}
