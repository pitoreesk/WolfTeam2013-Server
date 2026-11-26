package patcher

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOriginalLauncherRoutesOnly(t *testing.T) {
	server := newTestServer(t, Options{LauncherVersion: 20, GameVersion: 377, PatchDir: t.TempDir()})
	for _, route := range []string{"/Fetch.dll?1000&20", "/fetch.dll?2000&377", "/launcher.aspx", "/WSLuncher/InspTime.aspx", "/healthz"} {
		request := httptest.NewRequest(http.MethodGet, route, nil)
		response := httptest.NewRecorder()
		server.handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("GET %s returned %d", route, response.Code)
		}
		switch route {
		case "/Fetch.dll?1000&20":
			assertBody(t, route, response.Body.String(), "~0;0;20\r\n+Up to date\r\n")
		case "/fetch.dll?2000&377":
			assertBody(t, route, response.Body.String(), "~0;0;377\r\n+Up to date\r\n")
		}
	}
	for _, route := range []string{"/api/login.php", "/api/register.php", "/legacy/users.php"} {
		request := httptest.NewRequest(http.MethodPost, route, nil)
		response := httptest.NewRecorder()
		server.handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("POST %s returned %d, want 404", route, response.Code)
		}
	}
}

func TestFetchRejectsUnsupportedVersion(t *testing.T) {
	server := newTestServer(t, Options{LauncherVersion: 20, GameVersion: 377, PatchDir: t.TempDir()})
	for _, route := range []string{"/Fetch.dll?1000&19", "/Fetch.dll?1000&21", "/Fetch.dll?2000&0", "/Fetch.dll?2000&378", "/Fetch.dll?3000&0", "/Fetch.dll", "/Fetch.dll?bad&20"} {
		request := httptest.NewRequest(http.MethodGet, route, nil)
		response := httptest.NewRecorder()
		server.handler.ServeHTTP(response, request)
		if got := response.Body.String(); len(got) < len("Syntax Error") || got[:len("Syntax Error")] != "Syntax Error" {
			t.Errorf("GET %s body = %q, want Syntax Error", route, got)
		}
	}
}

func TestFetchPublishesCatalogPatchesAndFiles(t *testing.T) {
	directory := t.TempDir()
	launcherBytes := writeTestXFS(t, filepath.Join(directory, "launcher_21.xfs"), 2, 1)
	wolfBytes := writeTestXFS(t, filepath.Join(directory, "wolf_378.xfs"), 1, 378)
	server := newTestServer(t, Options{LauncherVersion: 20, GameVersion: 377, PatchDir: directory})

	tests := []struct {
		route string
		want  string
	}{
		{
			route: "http://patch.example/Fetch.dll?1000&20",
			want: fmt.Sprintf(
				"~1;%d;21\r\nM;LauncherEnc.xfs;http://patch.example/patches/launcher_21.xfs;1;%d\r\n",
				len(launcherBytes), len(launcherBytes),
			),
		},
		{
			route: "http://patch.example/Fetch.dll?2000&377",
			want: fmt.Sprintf(
				"~1;%d;378\r\nM;wolf.xfs;http://patch.example/patches/wolf_378.xfs;378;%d\r\n",
				len(wolfBytes), len(wolfBytes),
			),
		},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.route, nil)
		response := httptest.NewRecorder()
		server.handler.ServeHTTP(response, request)
		assertBody(t, test.route, response.Body.String(), test.want)
	}

	request := httptest.NewRequest(http.MethodGet, "http://patch.example/patches/wolf_378.xfs", nil)
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("patch download returned %d", response.Code)
	}
	if !bytes.Equal(response.Body.Bytes(), wolfBytes) {
		t.Fatal("patch download body differs from catalog file")
	}
	if got := response.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("patch Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("patch Cache-Control = %q", got)
	}
}

func TestFetchReturnsAllNewerPatchesInOrder(t *testing.T) {
	directory := t.TempDir()
	first := writeTestXFS(t, filepath.Join(directory, "wolf_378.xfs"), 1, 378)
	second := writeTestXFS(t, filepath.Join(directory, "wolf_379.xfs"), 1, 379)
	server := newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        directory,
		PublicURL:       "https://cdn.example/game",
	})
	request := httptest.NewRequest(http.MethodGet, "http://ignored.example/Fetch.dll?2000&377", nil)
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)
	want := fmt.Sprintf(
		"~2;%d;379\r\n"+
			"M;wolf.xfs;https://cdn.example/game/patches/wolf_378.xfs;378;%d\r\n"+
			"M;wolf.xfs;https://cdn.example/game/patches/wolf_379.xfs;379;%d\r\n",
		len(first)+len(second), len(first), len(second),
	)
	assertBody(t, request.URL.String(), response.Body.String(), want)
}

func TestPatchDownloadOnlyServesCatalogFiles(t *testing.T) {
	server := newTestServer(t, Options{LauncherVersion: 20, GameVersion: 377, PatchDir: t.TempDir()})
	for _, route := range []string{"/patches/", "/patches/missing.xfs", "/patches/../go.mod"} {
		request := httptest.NewRequest(http.MethodGet, route, nil)
		response := httptest.NewRecorder()
		server.handler.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			t.Errorf("GET %s unexpectedly returned 200", route)
		}
	}
}

func TestPatchDownloadSupportsLegacyResumeAndBypassesConditionalCache(t *testing.T) {
	directory := t.TempDir()
	patchBytes := writeTestXFS(t, filepath.Join(directory, "wolf_378.xfs"), 1, 378)
	server := newTestServer(t, Options{LauncherVersion: 20, GameVersion: 377, PatchDir: directory})

	resumeRequest := httptest.NewRequest(http.MethodGet, "/patches/wolf_378.xfs", nil)
	resumeRequest.Header.Set("Range", "bytes=1-")
	resumeResponse := httptest.NewRecorder()
	server.handler.ServeHTTP(resumeResponse, resumeRequest)
	if resumeResponse.Code != http.StatusPartialContent {
		t.Fatalf("range download returned %d", resumeResponse.Code)
	}
	if !bytes.Equal(resumeResponse.Body.Bytes(), patchBytes[1:]) {
		t.Fatal("range download body differs from requested patch suffix")
	}
	if got := resumeResponse.Header().Get("Content-Range"); got != fmt.Sprintf("bytes 1-%d/%d", len(patchBytes)-1, len(patchBytes)) {
		t.Errorf("Content-Range = %q", got)
	}

	conditionalRequest := httptest.NewRequest(http.MethodGet, "/patches/wolf_378.xfs", nil)
	conditionalRequest.Header.Set("If-Modified-Since", time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat))
	conditionalResponse := httptest.NewRecorder()
	server.handler.ServeHTTP(conditionalResponse, conditionalRequest)
	if conditionalResponse.Code != http.StatusOK {
		t.Fatalf("conditional legacy download returned %d, want 200", conditionalResponse.Code)
	}
	if !bytes.Equal(conditionalResponse.Body.Bytes(), patchBytes) {
		t.Fatal("conditional legacy download did not return the full patch")
	}
}

func newTestServer(t *testing.T, options Options) *Server {
	t.Helper()
	server, err := NewServer("unused", options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func writeTestXFS(t *testing.T, path string, version int, generation uint32) []byte {
	t.Helper()
	header := make([]byte, 64)
	if version == 1 {
		copy(header, "XFS ")
	} else {
		copy(header, "XFS2")
	}
	binary.LittleEndian.PutUint32(header[4:8], generation)
	binary.LittleEndian.PutUint32(header[16:20], 4)
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(header); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() > 255 {
		t.Fatalf("test header compressed to %d bytes", compressed.Len())
	}
	archive := make([]byte, 4, 5+compressed.Len())
	binary.LittleEndian.PutUint32(archive, 4)
	archive = append(archive, byte(compressed.Len()))
	archive = append(archive, compressed.Bytes()...)
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	return archive
}

func assertBody(t *testing.T, route, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("GET %s body = %q, want %q", route, got, want)
	}
}
