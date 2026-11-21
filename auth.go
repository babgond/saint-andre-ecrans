package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var uuid = regexp.MustCompile("^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$")

type loginAttempt struct {
	Nonce    string
	Verifier string
	Expires  time.Time
}

type adminSession struct {
	Name    string
	Expires time.Time
}

type authState struct {
	mu       sync.Mutex
	origin   string
	tenant   string
	allowed  map[string]bool
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	attempts map[string]loginAttempt
	sessions map[string]adminSession
	recent   []time.Time
}

func (a *application) configureAuth(ctx context.Context) error {
	tenant := strings.ToLower(os.Getenv("ENTRA_TENANT_ID"))
	client := os.Getenv("ENTRA_CLIENT_ID")
	secret := os.Getenv("ENTRA_CLIENT_SECRET")
	admins := strings.FieldsFunc(strings.ToLower(os.Getenv("ENTRA_ADMIN_OBJECT_IDS")), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' })
	if !uuid.MatchString(tenant) || !uuid.MatchString(strings.ToLower(client)) || secret == "" || len(admins) == 0 {
		return errors.New("Configurez le tenant, le client, le secret et les Object ID Entra dans les variables d’environnement")
	}
	u, err := validOrigin(os.Getenv("PUBLIC_URL"))
	if err != nil {
		return err
	}
	allowed := make(map[string]bool)
	for _, id := range admins {
		if !uuid.MatchString(id) {
			return errors.New("Un Object ID administrateur Entra est invalide")
		}
		allowed[id] = true
	}
	issuer := "https://login.microsoftonline.com/" + tenant + "/v2.0"
	// The tenant-specific v2 endpoints are fixed. JWKS are fetched and rotated by the verifier.
	clientHTTP := &http.Client{Timeout: 15 * time.Second}
	oidcCtx := oidc.ClientContext(ctx, clientHTTP)
	keys := oidc.NewRemoteKeySet(oidcCtx, "https://login.microsoftonline.com/"+tenant+"/discovery/v2.0/keys")
	a.auth = authState{
		origin: u.Scheme + "://" + u.Host,
		tenant: tenant, allowed: allowed, attempts: map[string]loginAttempt{}, sessions: map[string]adminSession{},
		verifier: oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: client, SupportedSigningAlgs: []string{oidc.RS256}}),
		oauth: oauth2.Config{
			ClientID: client, ClientSecret: secret, RedirectURL: u.Scheme + "://" + u.Host + "/auth/callback",
			Endpoint: oauth2.Endpoint{
				AuthURL:   "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/authorize",
				TokenURL:  "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
			Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
		},
	}
	return nil
}

func randomToken() string { return base64.RawURLEncoding.EncodeToString(randomBytes(32)) }

func randomBytes(n int) []byte {
	key := make([]byte, n)
	rand.Read(key)
	return key
}

func cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "__Host-" + name, Value: value, Path: "/", MaxAge: age, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie("__Host-" + name)
	if err != nil {
		return ""
	}
	return c.Value
}

func (a *application) login(w http.ResponseWriter, r *http.Request) {
	state := randomToken()
	attempt := loginAttempt{Nonce: randomToken(), Verifier: oauth2.GenerateVerifier(), Expires: time.Now().Add(10 * time.Minute)}
	a.auth.mu.Lock()
	now := time.Now()
	recent := a.auth.recent[:0]
	for _, t := range a.auth.recent {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	a.auth.recent = recent
	for key, v := range a.auth.attempts {
		if v.Expires.Before(now) {
			delete(a.auth.attempts, key)
		}
	}
	if len(recent) >= 30 || len(a.auth.attempts) >= 250 {
		a.auth.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Trop de tentatives. Réessayez dans une minute.", 429)
		return
	}
	delete(a.auth.attempts, cookieValue(r, "ecrans-oidc"))
	a.auth.recent = append(recent, now)
	a.auth.attempts[state] = attempt
	a.auth.mu.Unlock()
	cookie(w, "ecrans-oidc", state, 600)
	http.Redirect(w, r, a.auth.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(attempt.Verifier), oidc.Nonce(attempt.Nonce)), http.StatusSeeOther)
}

func (a *application) callback(w http.ResponseWriter, r *http.Request) {
	state := cookieValue(r, "ecrans-oidc")
	cookie(w, "ecrans-oidc", "", -1)
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(r.URL.Query().Get("state"))) != 1 {
		http.Redirect(w, r, "/admin?connexion=refusee", 303)
		return
	}
	a.auth.mu.Lock()
	attempt, ok := a.auth.attempts[state]
	delete(a.auth.attempts, state)
	a.auth.mu.Unlock()
	code := r.URL.Query().Get("code")
	if !ok || attempt.Expires.Before(time.Now()) || code == "" {
		http.Redirect(w, r, "/admin?connexion=refusee", 303)
		return
	}
	name, err := a.identity(r.Context(), code, attempt)
	if err != nil {
		http.Redirect(w, r, "/admin?connexion=refusee", 303)
		return
	}
	token := randomToken()
	now := time.Now()
	a.auth.mu.Lock()
	for key, s := range a.auth.sessions {
		if !s.Expires.After(now) {
			delete(a.auth.sessions, key)
		}
	}
	delete(a.auth.sessions, cookieValue(r, "ecrans-session"))
	a.auth.sessions[token] = adminSession{Name: name, Expires: now.Add(8 * time.Hour)}
	a.auth.mu.Unlock()
	cookie(w, "ecrans-session", token, 8*60*60)
	http.Redirect(w, r, "/admin", 303)
}

func (a *application) identity(ctx context.Context, code string, attempt loginAttempt) (string, error) {
	exchange, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := a.auth.oauth.Exchange(exchange, code, oauth2.VerifierOption(attempt.Verifier))
	if err != nil {
		return "", err
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return "", errors.New("ID token manquant")
	}
	id, err := a.auth.verifier.Verify(exchange, raw)
	if err != nil {
		return "", err
	}
	var claims struct {
		Tenant          string `json:"tid"`
		Object          string `json:"oid"`
		Name            string `json:"name"`
		Email           string `json:"preferred_username"`
		AuthorizedParty string `json:"azp"`
	}
	if err := id.Claims(&claims); err != nil {
		return "", err
	}
	if id.Nonce != attempt.Nonce || !strings.EqualFold(claims.Tenant, a.auth.tenant) ||
		!a.auth.allowed[strings.ToLower(claims.Object)] ||
		(len(id.Audience) > 1 && claims.AuthorizedParty == "") ||
		(claims.AuthorizedParty != "" && claims.AuthorizedParty != a.auth.oauth.ClientID) {
		return "", errors.New("Identité Entra non autorisée")
	}
	if claims.Name != "" {
		return claims.Name, nil
	}
	if claims.Email != "" {
		return claims.Email, nil
	}
	return "Administration", nil
}

func (a *application) session(r *http.Request) adminSession {
	key := cookieValue(r, "ecrans-session")
	if key == "" {
		return adminSession{}
	}
	a.auth.mu.Lock()
	defer a.auth.mu.Unlock()
	s := a.auth.sessions[key]
	if !s.Expires.After(time.Now()) {
		delete(a.auth.sessions, key)
		return adminSession{}
	}
	return s
}

func (a *application) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.session(r).Expires.IsZero() {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				problem(w, 401, "Reconnectez-vous à l’administration.")
				return
			}
			http.Redirect(w, r, "/admin", 303)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && (r.Header.Get("Origin") != a.auth.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
			problem(w, 403, "Origine non autorisée.")
			return
		}
		next(w, r)
	}
}

func (a *application) adminPage(w http.ResponseWriter, r *http.Request) {
	if a.session(r).Expires.IsZero() {
		page("login.html")(w, r)
		return
	}
	page("admin.html")(w, r)
}

func (a *application) logout(w http.ResponseWriter, r *http.Request) {
	a.auth.mu.Lock()
	delete(a.auth.sessions, cookieValue(r, "ecrans-session"))
	a.auth.mu.Unlock()
	cookie(w, "ecrans-session", "", -1)
	http.Redirect(w, r, "/admin", 303)
}
