package patcher

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClientManifestPublishesStoredGzipWithoutPerRequestCompression(t *testing.T) {
	patchDirectory := t.TempDir()
	clientDirectory := filepath.Join(patchDirectory, "client", "img")
	if err := os.MkdirAll(clientDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("compressible client payload\n"), 256)
	storedPath := filepath.Join(clientDirectory, "client.bin.gz")
	stored, err := os.Create(storedPath)
	if err != nil {
		t.Fatal(err)
	}
	compressor := gzip.NewWriter(stored)
	if _, err := compressor.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stored.Close(); err != nil {
		t.Fatal(err)
	}
	storedBytes, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatal(err)
	}

	server := newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        patchDirectory,
	})
	manifest := requestClientManifest(t, server)
	if len(manifest.Files) != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
	sum := sha256.Sum256(content)
	file := manifest.Files[0]
	if file.Path != "img/client.bin" ||
		file.URL != "/client-files/img/client.bin" ||
		file.Size != int64(len(content)) ||
		file.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("compressed manifest file = %+v", file)
	}

	compressedRequest := httptest.NewRequest(http.MethodGet, file.URL, nil)
	compressedRequest.Header.Set("Accept-Encoding", "gzip")
	compressedResponse := httptest.NewRecorder()
	server.handler.ServeHTTP(compressedResponse, compressedRequest)
	if compressedResponse.Code != http.StatusOK ||
		compressedResponse.Header().Get("Content-Encoding") != "gzip" ||
		!bytes.Equal(compressedResponse.Body.Bytes(), storedBytes) {
		t.Fatalf(
			"stored gzip response status=%d encoding=%q size=%d want=%d",
			compressedResponse.Code,
			compressedResponse.Header().Get("Content-Encoding"),
			compressedResponse.Body.Len(),
			len(storedBytes),
		)
	}

	rawResponse := httptest.NewRecorder()
	server.handler.ServeHTTP(
		rawResponse,
		httptest.NewRequest(http.MethodGet, file.URL, nil),
	)
	if rawResponse.Code != http.StatusOK ||
		rawResponse.Header().Get("Content-Encoding") != "" ||
		!bytes.Equal(rawResponse.Body.Bytes(), content) {
		t.Fatalf(
			"raw response status=%d encoding=%q size=%d",
			rawResponse.Code,
			rawResponse.Header().Get("Content-Encoding"),
			rawResponse.Body.Len(),
		)
	}
}

func TestClientCatalogRejectsDuplicateRawAndCompressedTargets(t *testing.T) {
	patchDirectory := t.TempDir()
	clientDirectory := filepath.Join(patchDirectory, "client", "img")
	if err := os.MkdirAll(clientDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clientDirectory, "client.bin"), []byte("raw"), 0o600); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(writer, "compressed"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(clientDirectory, "client.bin.gz"),
		compressed.Bytes(),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClientCatalog(clientDirectory, 377); err == nil {
		t.Fatal("duplicate raw and compressed targets were accepted")
	}
}

func TestClientManifestPublishesFullFiles(t *testing.T) {
	patchDirectory := t.TempDir()
	clientDirectory := filepath.Join(patchDirectory, "client", "Root")
	if err := os.MkdirAll(clientDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("independent launcher file")
	if err := os.WriteFile(filepath.Join(clientDirectory, "community content.pkg"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        patchDirectory,
	})

	request := httptest.NewRequest(http.MethodGet, "/api/launcher/manifest", nil)
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("manifest returned %d", response.Code)
	}
	var manifest ClientManifest
	if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != 1 || manifest.Version != 377 || len(manifest.Files) != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
	sum := sha256.Sum256(content)
	file := manifest.Files[0]
	if file.Path != "Root/community content.pkg" ||
		file.URL != "/client-files/Root/community%20content.pkg" ||
		file.Size != int64(len(content)) ||
		file.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("manifest file = %+v", file)
	}

	download := httptest.NewRecorder()
	server.handler.ServeHTTP(
		download,
		httptest.NewRequest(http.MethodGet, file.URL, nil),
	)
	if download.Code != http.StatusOK {
		t.Fatalf("client file returned %d", download.Code)
	}
	if got := download.Body.Bytes(); string(got) != string(content) {
		t.Fatalf("client file = %q", got)
	}
}

func TestClientManifestWithoutDirectoryIsEmpty(t *testing.T) {
	server := newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        t.TempDir(),
	})
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/launcher/manifest", nil),
	)
	var manifest ClientManifest
	if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Files == nil || len(manifest.Files) != 0 {
		t.Fatalf("empty manifest files = %#v", manifest.Files)
	}
}

func TestClientFilesRejectUnknownAndTraversalPaths(t *testing.T) {
	server := newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        t.TempDir(),
	})
	for _, route := range []string{
		"/client-files/missing.xfs",
		"/client-files/../go.mod",
		"/client-files/",
	} {
		response := httptest.NewRecorder()
		server.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code == http.StatusOK {
			t.Errorf("GET %s unexpectedly returned 200", route)
		}
	}
}

func TestClientCatalogHotReloadsAddedUpdatedAndRemovedFiles(t *testing.T) {
	patchDirectory := t.TempDir()
	server := newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        patchDirectory,
	})
	if manifest := requestClientManifest(t, server); len(manifest.Files) != 0 {
		t.Fatalf("initial manifest files = %+v", manifest.Files)
	}

	clientDirectory := filepath.Join(patchDirectory, "client", "img")
	if err := os.MkdirAll(clientDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(clientDirectory, "client.bin")
	first := []byte("first client override")
	if err := os.WriteFile(target, first, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := requestClientManifest(t, server)
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "img/client.bin" {
		t.Fatalf("added manifest = %+v", manifest)
	}
	firstHash := manifest.Files[0].SHA256

	second := []byte("second and longer client override")
	if err := os.WriteFile(target, second, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest = requestClientManifest(t, server)
	if len(manifest.Files) != 1 ||
		manifest.Files[0].Size != int64(len(second)) ||
		manifest.Files[0].SHA256 == firstHash {
		t.Fatalf("updated manifest = %+v", manifest)
	}
	download := httptest.NewRecorder()
	server.handler.ServeHTTP(
		download,
		httptest.NewRequest(http.MethodGet, manifest.Files[0].URL, nil),
	)
	if download.Code != http.StatusOK || string(download.Body.Bytes()) != string(second) {
		t.Fatalf("updated download status=%d body=%q", download.Code, download.Body.Bytes())
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if manifest = requestClientManifest(t, server); len(manifest.Files) != 0 {
		t.Fatalf("removed manifest files = %+v", manifest.Files)
	}
	download = httptest.NewRecorder()
	server.handler.ServeHTTP(
		download,
		httptest.NewRequest(http.MethodGet, "/client-files/img/client.bin", nil),
	)
	if download.Code != http.StatusNotFound {
		t.Fatalf("removed download returned %d", download.Code)
	}
}

func TestClientCatalogKeepsLastValidSnapshotOnReloadFailure(t *testing.T) {
	patchDirectory := t.TempDir()
	clientDirectory := filepath.Join(patchDirectory, "client")
	if err := os.MkdirAll(clientDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clientDirectory, "known.dat"), []byte("known"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, Options{
		LauncherVersion: 20,
		GameVersion:     377,
		PatchDir:        patchDirectory,
	})

	if err := os.RemoveAll(clientDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientDirectory, []byte("invalid directory replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := requestClientManifest(t, server)
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "known.dat" {
		t.Fatalf("invalid reload replaced last good manifest: %+v", manifest)
	}

	if err := os.Remove(clientDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(clientDirectory, "img"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(clientDirectory, "img", "replacement.dat"),
		[]byte("replacement"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	manifest = requestClientManifest(t, server)
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "img/replacement.dat" {
		t.Fatalf("recovered reload manifest = %+v", manifest)
	}
}

func requestClientManifest(t *testing.T, server *Server) ClientManifest {
	t.Helper()
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/launcher/manifest", nil),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("manifest returned %d", response.Code)
	}
	var manifest ClientManifest
	if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
