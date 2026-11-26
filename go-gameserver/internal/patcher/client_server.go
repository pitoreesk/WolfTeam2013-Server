package patcher

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
)

func urlPathEscape(value string) string {
	return url.PathEscape(value)
}

func (s *Server) clientManifestHandler(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.refreshClientCatalog()
	catalog := s.clientCatalog.Load()
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	if request.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(writer).Encode(catalog.manifest); err != nil {
		s.logger.Warn("write client manifest", "error", err)
	}
}

func (s *Server) clientFileHandler(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.refreshClientCatalog()
	catalog := s.clientCatalog.Load()
	name := strings.TrimPrefix(request.URL.Path, "/client-files/")
	fileEntry, ok := catalog.byPath[strings.ToLower(name)]
	if !ok {
		http.NotFound(writer, request)
		return
	}
	file, err := os.Open(fileEntry.path)
	if err != nil {
		s.logger.Error("open client file", "file", fileEntry.Path, "error", err)
		http.Error(writer, "client file unavailable", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != fileEntry.storedSize {
		s.logger.Error("client file changed after startup", "file", fileEntry.Path, "error", err)
		http.Error(writer, "client file unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set(
		"Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", path.Base(fileEntry.Path)),
	)
	writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if fileEntry.contentEncoding == "gzip" {
		writer.Header().Set("Vary", "Accept-Encoding")
		if acceptsEncoding(request.Header.Get("Accept-Encoding"), "gzip") {
			writer.Header().Set("Content-Encoding", "gzip")
			writer.Header().Set("ETag", fmt.Sprintf("%q", "sha256-"+fileEntry.SHA256+"-gzip"))
			http.ServeContent(writer, request, info.Name(), info.ModTime(), file)
			return
		}
		reader, err := gzip.NewReader(file)
		if err != nil {
			s.logger.Error("open compressed client file", "file", fileEntry.Path, "error", err)
			http.Error(writer, "client file unavailable", http.StatusServiceUnavailable)
			return
		}
		defer reader.Close()
		writer.Header().Set("ETag", fmt.Sprintf("%q", "sha256-"+fileEntry.SHA256))
		writer.Header().Set("Content-Length", strconv.FormatInt(fileEntry.Size, 10))
		if request.Method == http.MethodHead {
			return
		}
		if _, err := io.Copy(writer, reader); err != nil {
			s.logger.Warn("write decompressed client file", "file", fileEntry.Path, "error", err)
		}
		return
	}
	writer.Header().Set("ETag", fmt.Sprintf("%q", "sha256-"+fileEntry.SHA256))
	http.ServeContent(writer, request, info.Name(), info.ModTime(), file)
}

func acceptsEncoding(header string, wanted string) bool {
	for _, value := range strings.Split(header, ",") {
		parts := strings.Split(value, ";")
		encoding := strings.TrimSpace(parts[0])
		if !strings.EqualFold(encoding, wanted) && encoding != "*" {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			name, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || !strings.EqualFold(strings.TrimSpace(name), "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				quality = 0
			} else {
				quality = parsed
			}
		}
		if quality > 0 {
			return true
		}
	}
	return false
}

// refreshClientCatalog atomically swaps in a new immutable snapshot when the
// mounted patch directory changes. Requests keep using the last valid snapshot
// if an update leaves an incomplete or invalid directory behind.
func (s *Server) refreshClientCatalog() {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()

	before, err := clientCatalogState(s.clientDir)
	if err != nil {
		s.logger.Warn("inspect client patch directory", "error", err)
		return
	}
	if before == s.clientState || before == s.clientFailed {
		return
	}
	catalog, err := LoadClientCatalog(s.clientDir, s.clientVersion)
	if err != nil {
		s.clientFailed = before
		s.logger.Warn("reload client patch catalog", "error", err)
		return
	}
	after, err := clientCatalogState(s.clientDir)
	if err != nil {
		s.logger.Warn("verify client patch directory", "error", err)
		return
	}
	if before != after {
		s.logger.Info("client patch directory changed during reload; retrying on next request")
		return
	}
	s.clientCatalog.Store(catalog)
	s.clientState = after
	s.clientFailed = ""
	s.logger.Info(
		"client patch catalog reloaded",
		"files", len(catalog.manifest.Files),
		"version", catalog.manifest.Version,
	)
}
