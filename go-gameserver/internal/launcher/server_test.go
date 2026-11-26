package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeAuthenticator struct {
	username string
	password string
}

func (f fakeAuthenticator) Login(_ context.Context, username, password string) (int, error) {
	if username == f.username && password == f.password {
		return 1, nil
	}
	return -1, nil
}

func TestSessionAuthenticationAndOneTimeStart(t *testing.T) {
	server := testServer()
	login := performJSON(
		t,
		server,
		http.MethodPost,
		SessionPath,
		`{"username":"admin","password":"test123"}`,
		"",
	)
	if login.Code != http.StatusCreated {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	var created sessionResponse
	if err := json.Unmarshal(login.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.ExpiresAt == "" {
		t.Fatalf("session response = %+v", created)
	}

	start := performJSON(t, server, http.MethodPost, StartSessionPath, "", created.Token)
	if start.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", start.Code, start.Body.String())
	}
	var started startResponse
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.ChannelID != 0x13 {
		t.Fatalf("channel ID = %#x", started.ChannelID)
	}

	replay := performJSON(t, server, http.MethodPost, StartSessionPath, "", created.Token)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed token status = %d", replay.Code)
	}
}

func TestSessionRejectsWrongPassword(t *testing.T) {
	server := testServer()
	response := performJSON(
		t,
		server,
		http.MethodPost,
		SessionPath,
		`{"username":"admin","password":"wrong"}`,
		"",
	)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-password status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestSessionRejectsMalformedAndUnknownFields(t *testing.T) {
	server := testServer()
	for _, body := range []string{
		`{"username":"admin"}`,
		`{"username":"admin","password":"test123","legacy":true}`,
		`{"username":"admin","password":"test123"} {}`,
	} {
		response := performJSON(t, server, http.MethodPost, SessionPath, body, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q returned %d", body, response.Code)
		}
	}
}

func TestExpiredSessionCannotStart(t *testing.T) {
	server := testServer()
	current := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return current }
	token, _, err := server.issueSession(1)
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(defaultSessionTTL + time.Second)
	response := performJSON(t, server, http.MethodPost, StartSessionPath, "", token)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d", response.Code)
	}
}

func testServer() *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(
		"unused",
		Options{ChannelID: 0x13},
		fakeAuthenticator{username: "admin", password: "test123"},
		logger,
	)
}

func performJSON(
	t *testing.T,
	server *Server,
	method string,
	path string,
	body string,
	token string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	return response
}
