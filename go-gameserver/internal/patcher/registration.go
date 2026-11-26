package patcher

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wtserver-go/internal/store"
)

const (
	registrationBodyLimit = 8 << 10
	registrationCSRFName  = "wt_registration_csrf"
	registrationMaxTries  = 5
	registrationWindow    = 10 * time.Minute
)

// AccountRegistrar is the narrow account operation exposed to the HTTP page.
// PostgresStore implements it with the same transaction used by wtadmin.
type AccountRegistrar interface {
	Register(context.Context, string, string) error
}

//go:embed web/registration.html
var registrationHTML string

//go:embed web/registration.css
var registrationCSS string

//go:embed web/registration.js
var registrationJS string

//go:embed web/registration_legacy.html
var registrationLegacyHTML string

//go:embed web/registration_legacy.css
var registrationLegacyCSS string

var publicUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,12}$`)

type registrationSite struct {
	registrar  AccountRegistrar
	logger     *slog.Logger
	page       *template.Template
	legacyPage *template.Template
	csrfSecret [32]byte
	limiter    *registrationLimiter
}

type registrationPage struct {
	CSRFToken  string
	Username   string
	Created    bool
	Error      string
	FieldError map[string]string
}

type registrationLimiter struct {
	mu        sync.Mutex
	attempts  map[string][]time.Time
	now       func() time.Time
	lastPrune time.Time
}

func newRegistrationSite(registrar AccountRegistrar, logger *slog.Logger) (*registrationSite, error) {
	page, err := template.New("registration").Parse(registrationHTML)
	if err != nil {
		return nil, err
	}
	legacyPage, err := template.New("registration-legacy").Parse(registrationLegacyHTML)
	if err != nil {
		return nil, err
	}
	var csrfSecret [32]byte
	if _, err := rand.Read(csrfSecret[:]); err != nil {
		return nil, err
	}
	return &registrationSite{
		registrar:  registrar,
		logger:     logger,
		page:       page,
		legacyPage: legacyPage,
		csrfSecret: csrfSecret,
		limiter: &registrationLimiter{
			attempts: make(map[string][]time.Time),
			now:      time.Now,
		},
	}, nil
}

func (s *registrationSite) home(writer http.ResponseWriter, request *http.Request) {
	registrationResponseHeaders(writer.Header(), isLegacyRegistration(request))
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.render(writer, request, http.StatusOK, registrationPage{})
}

func (s *registrationSite) register(writer http.ResponseWriter, request *http.Request) {
	registrationResponseHeaders(writer.Header(), isLegacyRegistration(request))
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		s.render(writer, request, http.StatusOK, registrationPage{Created: request.URL.Query().Get("created") == "1"})
	case http.MethodPost:
		s.createAccount(writer, request)
	default:
		writer.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *registrationSite) launcher(writer http.ResponseWriter, request *http.Request) {
	registrationResponseHeaders(writer.Header(), true)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.renderLegacy(writer, request, http.StatusOK, registrationPage{Created: request.URL.Query().Get("created") == "1"})
}

func (s *registrationSite) createAccount(writer http.ResponseWriter, request *http.Request) {
	allowed, retryAfter := s.limiter.allow(clientAddress(request.RemoteAddr))
	if !allowed {
		writer.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
		s.render(writer, request, http.StatusTooManyRequests, registrationPage{
			Error: "Too many registration attempts. Please wait a few minutes and try again.",
		})
		return
	}

	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		s.render(writer, request, http.StatusUnsupportedMediaType, registrationPage{
			Error: "The registration form could not be read. Please refresh the page and try again.",
		})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, registrationBodyLimit)
	if err := request.ParseForm(); err != nil {
		s.render(writer, request, http.StatusBadRequest, registrationPage{
			Error: "The registration form was too large or malformed. Please refresh and try again.",
		})
		return
	}

	username := strings.TrimSpace(request.PostForm.Get("username"))
	page := registrationPage{Username: username, FieldError: make(map[string]string)}
	validToken := validCSRF(request)
	if isLegacyRegistration(request) {
		validToken = validSignedCSRF(s.csrfSecret[:], request.PostForm.Get("csrf_token"), time.Now())
	}
	if !validToken {
		page.Error = "This form expired. Please review your details and submit it again."
		s.render(writer, request, http.StatusForbidden, page)
		return
	}

	password := request.PostForm.Get("password")
	confirmation := request.PostForm.Get("password_confirmation")
	if !publicUsernamePattern.MatchString(username) {
		page.FieldError["username"] = "Use 3–20 letters, numbers, or underscores."
	}
	if len(password) < 8 || len(password) > 64 {
		page.FieldError["password"] = "Use a password between 8 and 64 bytes."
	}
	if confirmation != password {
		page.FieldError["password_confirmation"] = "The passwords do not match."
	}
	if request.PostForm.Get("account_notice") != "accepted" {
		page.FieldError["account_notice"] = "Please confirm before creating your account."
	}
	if len(page.FieldError) != 0 {
		page.Error = "Please correct the highlighted fields."
		s.render(writer, request, http.StatusUnprocessableEntity, page)
		return
	}
	if s.registrar == nil {
		page.Error = "Registration is temporarily unavailable. Please try again later."
		s.render(writer, request, http.StatusServiceUnavailable, page)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	if err := s.registrar.Register(ctx, username, password); err != nil {
		switch {
		case errors.Is(err, store.ErrUsernameTaken):
			page.FieldError["username"] = "That username is already registered."
			page.Error = "Choose a different username and try again."
			s.render(writer, request, http.StatusConflict, page)
		case errors.Is(err, store.ErrInvalidUsername):
			page.FieldError["username"] = "Use 3–20 letters, numbers, or underscores."
			page.Error = "Please correct the highlighted field."
			s.render(writer, request, http.StatusUnprocessableEntity, page)
		case errors.Is(err, store.ErrInvalidPassword):
			page.FieldError["password"] = "Use a password between 8 and 64 bytes."
			page.Error = "Please correct the highlighted field."
			s.render(writer, request, http.StatusUnprocessableEntity, page)
		default:
			s.logger.Error("register account from web page", "username", username, "error", err)
			page.Error = "Registration is temporarily unavailable. Please try again later."
			s.render(writer, request, http.StatusServiceUnavailable, page)
		}
		return
	}

	s.logger.Info("account registered from web page", "username", username)
	location := "/register?created=1"
	if isLegacyRegistration(request) {
		location = "/register?legacy=1&created=1"
	} else {
		http.SetCookie(writer, &http.Cookie{
			Name: registrationCSRFName, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, Secure: request.TLS != nil, SameSite: http.SameSiteLaxMode,
		})
	}
	http.Redirect(writer, request, location, http.StatusSeeOther)
}

func (s *registrationSite) render(writer http.ResponseWriter, request *http.Request, status int, page registrationPage) {
	if isLegacyRegistration(request) {
		s.renderLegacy(writer, request, status, page)
		return
	}
	token, err := newCSRFToken()
	if err != nil {
		s.logger.Error("generate registration CSRF token", "error", err)
		http.Error(writer, "registration unavailable", http.StatusInternalServerError)
		return
	}
	page.CSRFToken = token
	if page.FieldError == nil {
		page.FieldError = make(map[string]string)
	}
	var body bytes.Buffer
	if err := s.page.Execute(&body, page); err != nil {
		s.logger.Error("render registration page", "error", err)
		http.Error(writer, "registration unavailable", http.StatusInternalServerError)
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: registrationCSRFName, Value: token, Path: "/", MaxAge: 3600,
		HttpOnly: true, Secure: request.TLS != nil, SameSite: http.SameSiteLaxMode,
	})
	registrationSecurityHeaders(writer.Header())
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(body.Bytes())
	}
}

func (s *registrationSite) renderLegacy(writer http.ResponseWriter, request *http.Request, status int, page registrationPage) {
	token, err := newSignedCSRF(s.csrfSecret[:], time.Now())
	if err != nil {
		s.logger.Error("generate legacy registration CSRF token", "error", err)
		http.Error(writer, "registration unavailable", http.StatusInternalServerError)
		return
	}
	page.CSRFToken = token
	if page.FieldError == nil {
		page.FieldError = make(map[string]string)
	}
	var body bytes.Buffer
	if err := s.legacyPage.Execute(&body, page); err != nil {
		s.logger.Error("render legacy registration page", "error", err)
		http.Error(writer, "registration unavailable", http.StatusInternalServerError)
		return
	}
	writeLegacyResponse(writer, request, status, "text/html; charset=utf-8", body.Bytes())
}

func (s *registrationSite) stylesheet(writer http.ResponseWriter, request *http.Request) {
	s.serveAsset(writer, request, "text/css; charset=utf-8", registrationCSS)
}

func (s *registrationSite) javascript(writer http.ResponseWriter, request *http.Request) {
	s.serveAsset(writer, request, "text/javascript; charset=utf-8", registrationJS)
}

func (s *registrationSite) legacyStylesheet(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeLegacyResponse(
		writer, request, http.StatusOK, "text/css; charset=utf-8", []byte(registrationLegacyCSS),
	)
}

func writeLegacyResponse(
	writer http.ResponseWriter,
	request *http.Request,
	status int,
	contentType string,
	body []byte,
) {
	registrationResponseHeaders(writer.Header(), true)
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.Header().Set("Connection", "close")
	writer.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(body)
	}
}

func (s *registrationSite) serveAsset(writer http.ResponseWriter, request *http.Request, contentType, asset string) {
	registrationSecurityHeaders(writer.Header())
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	if request.Method != http.MethodHead {
		_, _ = io.WriteString(writer, asset)
	}
}

func registrationSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'self'")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "SAMEORIGIN")
}

func registrationResponseHeaders(header http.Header, legacy bool) {
	header.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
	if legacy {
		header.Del("Content-Security-Policy")
		header.Del("Cross-Origin-Opener-Policy")
		header.Del("Permissions-Policy")
		header.Del("X-Frame-Options")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-UA-Compatible", "IE=EmulateIE7")
		return
	}
	registrationSecurityHeaders(header)
}

func newCSRFToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validCSRF(request *http.Request) bool {
	cookie, err := request.Cookie(registrationCSRFName)
	if err != nil {
		return false
	}
	formToken := request.PostForm.Get("csrf_token")
	if len(cookie.Value) != 43 || len(formToken) != len(cookie.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(formToken)) == 1
}

func newSignedCSRF(secret []byte, now time.Time) (string, error) {
	payload := make([]byte, 24)
	binary.BigEndian.PutUint64(payload[:8], uint64(now.Unix()))
	if _, err := rand.Read(payload[8:]); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...)), nil
}

func validSignedCSRF(secret []byte, token string, now time.Time) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 56 {
		return false
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(decoded[:8])), 0)
	if issued.After(now.Add(time.Minute)) || now.Sub(issued) > time.Hour {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(decoded[:24])
	return hmac.Equal(decoded[24:], mac.Sum(nil))
}

func isLegacyRegistration(request *http.Request) bool {
	if request.URL.Path == "/launcher.aspx" || request.URL.Query().Get("legacy") == "1" {
		return true
	}
	userAgent := strings.ToUpper(request.UserAgent())
	return strings.Contains(userAgent, "MSIE 5.") || strings.Contains(userAgent, "MSIE 6.")
}

func clientAddress(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil && host != "" {
		return host
	}
	return remoteAddress
}

func (l *registrationLimiter) allow(address string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-registrationWindow)
	if l.lastPrune.IsZero() || now.Sub(l.lastPrune) >= registrationWindow {
		for client, attempts := range l.attempts {
			active := attempts[:0]
			for _, attempt := range attempts {
				if attempt.After(cutoff) {
					active = append(active, attempt)
				}
			}
			if len(active) == 0 {
				delete(l.attempts, client)
			} else {
				l.attempts[client] = active
			}
		}
		l.lastPrune = now
	}
	existing := l.attempts[address]
	kept := existing[:0]
	for _, attempt := range existing {
		if attempt.After(cutoff) {
			kept = append(kept, attempt)
		}
	}
	if len(kept) >= registrationMaxTries {
		return false, max(time.Second, kept[0].Add(registrationWindow).Sub(now))
	}
	l.attempts[address] = append(kept, now)
	return true, 0
}
