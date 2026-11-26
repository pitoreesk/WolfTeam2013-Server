package patcher

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"wtserver-go/internal/store"
)

type registrationRecorder struct {
	username string
	password string
	err      error
	calls    int
}

func (r *registrationRecorder) Register(_ context.Context, username, password string) error {
	r.calls++
	r.username = username
	r.password = password
	return r.err
}

func TestRegistrationPage(t *testing.T) {
	server := newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        t.TempDir(),
		Registrar:       &registrationRecorder{},
	})
	request := httptest.NewRequest(http.MethodGet, "/register", nil)
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET /register returned %d", response.Code)
	}
	for _, content := range []string{
		"Create account",
		`action="/register"`,
		`name="csrf_token"`,
		"INITIAL DEPLOYMENT KIT",
		"<strong>02</strong><span>Characters</span>",
		"default red and blue characters",
	} {
		if !strings.Contains(response.Body.String(), content) {
			t.Errorf("registration page does not contain %q", content)
		}
	}
	if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "form-action 'self'") {
		t.Errorf("Content-Security-Policy = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if cookie := registrationCookie(t, response); !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("CSRF cookie missing protections: %#v", cookie)
	}
}

func TestLegacyRegistrationPageWorksWithoutCookies(t *testing.T) {
	recorder := &registrationRecorder{}
	server := newRegistrationTestServer(t, recorder)
	request := httptest.NewRequest(http.MethodGet, "/launcher.aspx", nil)
	request.Header.Set("User-Agent", "Mozilla/4.0 (compatible; MSIE 6.0; Windows NT 5.1)")
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET /launcher.aspx returned %d", response.Code)
	}
	for _, content := range []string{
		`<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN"`,
		`action="/register?legacy=1"`,
		`/assets/registration-legacy.css`,
		`name="csrf_token"`,
	} {
		if !strings.Contains(response.Body.String(), content) {
			t.Errorf("legacy registration page does not contain %q", content)
		}
	}
	if got := response.Header().Get("X-UA-Compatible"); got != "IE=EmulateIE7" {
		t.Errorf("X-UA-Compatible = %q", got)
	}
	if got := response.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("legacy X-Frame-Options = %q, want empty for launcher iframe", got)
	}
	if got := response.Header().Get("Content-Security-Policy"); got != "" {
		t.Errorf("legacy Content-Security-Policy = %q, want empty for launcher iframe", got)
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("legacy registration unexpectedly requires cookies: %#v", cookies)
	}
	if got, want := response.Header().Get("Content-Length"), strconv.Itoa(response.Body.Len()); got != want {
		t.Errorf("legacy Content-Length = %q, want %q", got, want)
	}
	if got := response.Header().Get("Connection"); got != "close" {
		t.Errorf("legacy Connection = %q, want close", got)
	}

	values := registrationForm("Legacy_Wolf", "Secret!123", "Secret!123", true)
	values.Set("csrf_token", registrationToken(t, response.Body.String()))
	post := httptest.NewRequest(http.MethodPost, "/register?legacy=1", strings.NewReader(values.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("User-Agent", "Mozilla/4.0 (compatible; MSIE 6.0; Windows NT 5.1)")
	post.RemoteAddr = "192.0.2.20:9000"
	postResponse := httptest.NewRecorder()
	server.handler.ServeHTTP(postResponse, post)

	if postResponse.Code != http.StatusSeeOther {
		t.Fatalf("legacy POST /register returned %d: %s", postResponse.Code, postResponse.Body.String())
	}
	if got := postResponse.Header().Get("Location"); got != "/register?legacy=1&created=1" {
		t.Errorf("legacy Location = %q", got)
	}
	if recorder.calls != 1 || recorder.username != "Legacy_Wolf" {
		t.Errorf("legacy registrar call = %d, %q", recorder.calls, recorder.username)
	}
}

func TestRegistrationAssets(t *testing.T) {
	server := newRegistrationTestServer(t, &registrationRecorder{})
	for _, test := range []struct {
		path        string
		contentType string
		content     string
	}{
		{path: "/assets/registration.css", contentType: "text/css; charset=utf-8", content: ".registration-panel"},
		{path: "/assets/registration.js", contentType: "text/javascript; charset=utf-8", content: "data-registration-form"},
		{path: "/assets/registration-legacy.css", contentType: "text/css; charset=utf-8", content: ".legacy-form-cell"},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		server.handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("GET %s returned %d", test.path, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != test.contentType {
			t.Errorf("GET %s Content-Type = %q", test.path, got)
		}
		if !strings.Contains(response.Body.String(), test.content) {
			t.Errorf("GET %s body does not contain %q", test.path, test.content)
		}
		if test.path == "/assets/registration-legacy.css" {
			if got, want := response.Header().Get("Content-Length"), strconv.Itoa(response.Body.Len()); got != want {
				t.Errorf("GET %s Content-Length = %q, want %q", test.path, got, want)
			}
			if got := response.Header().Get("Connection"); got != "close" {
				t.Errorf("GET %s Connection = %q, want close", test.path, got)
			}
		}
	}
}

func TestSignedCSRFRejectsTamperingAndExpiry(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	token, err := newSignedCSRF(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if !validSignedCSRF(secret, token, now.Add(30*time.Minute)) {
		t.Fatal("valid signed CSRF token was rejected")
	}
	tamperedBytes := []byte(token)
	tamperedIndex := len(tamperedBytes) / 2
	if tamperedBytes[tamperedIndex] == 'A' {
		tamperedBytes[tamperedIndex] = 'B'
	} else {
		tamperedBytes[tamperedIndex] = 'A'
	}
	tampered := string(tamperedBytes)
	if validSignedCSRF(secret, tampered, now) {
		t.Fatal("tampered signed CSRF token was accepted")
	}
	if validSignedCSRF(secret, token, now.Add(61*time.Minute)) {
		t.Fatal("expired signed CSRF token was accepted")
	}
}

func TestRegistrationCreatesAccount(t *testing.T) {
	recorder := &registrationRecorder{}
	server := newRegistrationTestServer(t, recorder)
	response := submitRegistration(t, server, registrationForm("Alpha_Wolf", "Secret!123", "Secret!123", true), "192.0.2.10:9000")

	if response.Code != http.StatusSeeOther {
		t.Fatalf("POST /register returned %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != "/register?created=1" {
		t.Errorf("Location = %q", got)
	}
	if recorder.calls != 1 || recorder.username != "Alpha_Wolf" || recorder.password != "Secret!123" {
		t.Errorf("registrar call = %d, %q, %q", recorder.calls, recorder.username, recorder.password)
	}
}

func TestRegistrationRejectsInvalidSubmission(t *testing.T) {
	recorder := &registrationRecorder{}
	server := newRegistrationTestServer(t, recorder)
	response := submitRegistration(t, server, registrationForm("bad name", "short", "different", false), "192.0.2.11:9000")

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /register returned %d: %s", response.Code, response.Body.String())
	}
	for _, message := range []string{"Use 3–20 letters", "between 8 and 64 bytes", "passwords do not match", "Please confirm"} {
		if !strings.Contains(response.Body.String(), message) {
			t.Errorf("validation response does not contain %q", message)
		}
	}
	if recorder.calls != 0 {
		t.Errorf("registrar called %d times for invalid form", recorder.calls)
	}
}

func TestRegistrationRejectsMissingCSRFToken(t *testing.T) {
	recorder := &registrationRecorder{}
	server := newRegistrationTestServer(t, recorder)
	values := registrationForm("Alpha_Wolf", "Secret!123", "Secret!123", true)
	request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("POST /register without CSRF cookie returned %d", response.Code)
	}
	if recorder.calls != 0 {
		t.Errorf("registrar called %d times without CSRF token", recorder.calls)
	}
}

func TestRegistrationReportsDuplicateUsername(t *testing.T) {
	recorder := &registrationRecorder{err: store.ErrUsernameTaken}
	server := newRegistrationTestServer(t, recorder)
	response := submitRegistration(t, server, registrationForm("Alpha_Wolf", "Secret!123", "Secret!123", true), "192.0.2.12:9000")

	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate registration returned %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "already registered") {
		t.Fatal("duplicate registration did not explain that the username is registered")
	}
}

func TestRegistrationHidesStorageFailure(t *testing.T) {
	recorder := &registrationRecorder{err: errors.New("database host and password must not leak")}
	server := newRegistrationTestServer(t, recorder)
	response := submitRegistration(t, server, registrationForm("Alpha_Wolf", "Secret!123", "Secret!123", true), "192.0.2.13:9000")

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed registration returned %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "database host") {
		t.Fatal("storage error leaked into registration response")
	}
}

func TestRegistrationRateLimit(t *testing.T) {
	recorder := &registrationRecorder{}
	server := newRegistrationTestServer(t, recorder)
	for attempt := 0; attempt < registrationMaxTries; attempt++ {
		response := submitRegistration(t, server, registrationForm("bad name", "Secret!123", "Secret!123", true), "192.0.2.14:9000")
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d returned %d", attempt+1, response.Code)
		}
	}
	response := submitRegistration(t, server, registrationForm("Alpha_Wolf", "Secret!123", "Secret!123", true), "192.0.2.14:9000")
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited attempt returned %d", response.Code)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited response is missing Retry-After")
	}
}

func newRegistrationTestServer(t *testing.T, registrar AccountRegistrar) *Server {
	t.Helper()
	return newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        t.TempDir(),
		Registrar:       registrar,
	})
}

func submitRegistration(t *testing.T, server *Server, values url.Values, remoteAddress string) *httptest.ResponseRecorder {
	t.Helper()
	getRequest := httptest.NewRequest(http.MethodGet, "/register", nil)
	getResponse := httptest.NewRecorder()
	server.handler.ServeHTTP(getResponse, getRequest)
	cookie := registrationCookie(t, getResponse)
	values.Set("csrf_token", cookie.Value)

	request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = remoteAddress
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)
	return response
}

func registrationCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == registrationCSRFName {
			return cookie
		}
	}
	t.Fatal("registration response did not set a CSRF cookie")
	return nil
}

func registrationToken(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="csrf_token" value="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatal("registration response did not include a CSRF token")
	}
	start += len(marker)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		t.Fatal("registration CSRF token was not terminated")
	}
	return body[start : start+end]
}

func registrationForm(username, password, confirmation string, accepted bool) url.Values {
	values := url.Values{
		"username":              {username},
		"password":              {password},
		"password_confirmation": {confirmation},
	}
	if accepted {
		values.Set("account_notice", "accepted")
	}
	return values
}
