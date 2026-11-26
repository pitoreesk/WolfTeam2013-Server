package patcher

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestApexWebServesFilesCaseInsensitively(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "web")
	for _, sub := range []string{"pic", "AXUP"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"Config.ini":    "[url]",
		"pic/bg.png":    "png",
		"AXUP/update.u": "u",
		"../secret.txt": "secret",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler := apexWebHandler(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for path, want := range map[string]string{
		ApexWebPath + "Config.ini":               "[url]",
		ApexWebPath + "config.INI":               "[url]",
		ApexWebPath + "pic/bg.png":               "png",
		ApexWebPath + "PIC/BG.PNG":               "png",
		ApexWebPath + "axup/update.u":            "u",
		ApexWebPath:                              "",
		ApexWebPath + "pic":                      "",
		ApexWebPath + "pic/":                     "",
		ApexWebPath + "..%2fsecret.txt":          "",
		ApexWebPath + "pic/..%2f..%2fsecret.txt": "",
		ApexWebPath + "pic%5cbg.png":             "",
		ApexWebPath + "missing.dll":              "",
		ApexWebPath + "Config.ini/x":             "",
	} {
		recorder := httptest.NewRecorder()
		handler(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		wantCode := http.StatusOK
		if want == "" {
			wantCode = http.StatusNotFound
		}
		if recorder.Code != wantCode {
			t.Fatalf("%s = %d, want %d", path, recorder.Code, wantCode)
		}
		if want != "" && recorder.Body.String() != want {
			t.Fatalf("%s body = %q", path, recorder.Body.String())
		}
	}
}
