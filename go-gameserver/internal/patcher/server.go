package patcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	LauncherVersion int
	GameVersion     int
	PatchDir        string
	PublicURL       string
	ApexWebDir      string
	NoticeWebDir    string
	Registrar       AccountRegistrar
}

type Server struct {
	address       string
	logger        *slog.Logger
	handler       http.Handler
	catalog       *Catalog
	clientCatalog atomic.Pointer[ClientCatalog]
	clientDir     string
	clientVersion int
	clientState   string
	clientFailed  string
	clientMu      sync.Mutex
	publicURL     *url.URL
}

func NewServer(address string, options Options, logger *slog.Logger) (*Server, error) {
	catalog, err := LoadCatalog(options.PatchDir, options.LauncherVersion, options.GameVersion)
	if err != nil {
		return nil, err
	}
	clientDir := filepath.Join(options.PatchDir, "client")
	clientCatalog, err := LoadClientCatalog(clientDir, options.GameVersion)
	if err != nil {
		return nil, fmt.Errorf("client file catalog: %w", err)
	}
	clientState, err := clientCatalogState(clientDir)
	if err != nil {
		return nil, fmt.Errorf("client file catalog state: %w", err)
	}
	var publicURL *url.URL
	if options.PublicURL != "" {
		publicURL, err = url.Parse(options.PublicURL)
		if err != nil {
			return nil, fmt.Errorf("parse patch public URL: %w", err)
		}
		if (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" || strings.ContainsAny(options.PublicURL, ";\r\n") {
			return nil, fmt.Errorf("patch public URL %q must be an absolute HTTP(S) URL without credentials, query, or fragment", options.PublicURL)
		}
	}
	server := &Server{
		address: address, logger: logger, catalog: catalog,
		clientDir: clientDir, clientVersion: options.GameVersion,
		clientState: clientState, publicURL: publicURL,
	}
	server.clientCatalog.Store(clientCatalog)
	registration, err := newRegistrationSite(options.Registrar, logger)
	if err != nil {
		return nil, fmt.Errorf("registration page: %w", err)
	}
	mux := http.NewServeMux()
	serveFetch := server.fetchHandler()
	mux.HandleFunc("/Fetch.dll", serveFetch)
	mux.HandleFunc("/fetch.dll", serveFetch)
	mux.HandleFunc("/patches/", server.patchHandler)
	mux.HandleFunc("/api/launcher/manifest", server.clientManifestHandler)
	mux.HandleFunc("/client-files/", server.clientFileHandler)
	mux.HandleFunc("/launcher.aspx", registration.launcher)
	mux.HandleFunc("/WSLuncher/InspTime.aspx", inspectionTime)
	mux.HandleFunc("/healthz", health)
	if options.ApexWebDir != "" {
		mux.HandleFunc(ApexWebPath, apexWebHandler(options.ApexWebDir, logger))
	}
	if options.NoticeWebDir != "" {
		mux.HandleFunc(NoticeWebPath, noticeWebHandler(options.NoticeWebDir, logger))
	}
	mux.HandleFunc("/assets/registration.css", registration.stylesheet)
	mux.HandleFunc("/assets/registration.js", registration.javascript)
	mux.HandleFunc("/assets/registration-legacy.css", registration.legacyStylesheet)
	mux.HandleFunc("/register", registration.register)
	mux.HandleFunc("/", registration.home)
	server.handler = mux
	return server, nil
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.address)
	if err != nil {
		return fmt.Errorf("listen for launcher patch requests: %w", err)
	}
	server := &http.Server{
		Handler: s.handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	s.logger.Info("launcher patch service started", "address", listener.Addr().String(), "patches", s.catalog.Count())
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		return err
	}
	return nil
}

func (s *Server) fetchHandler() http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		appID, clientVersion, err := parseFetchQuery(request.URL.RawQuery)
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		if err != nil {
			s.logger.Warn("invalid launcher patch request", "query", request.URL.RawQuery, "error", err)
			writeFetchError(writer, "invalid request")
			return
		}
		latestVersion, ok := s.catalog.LatestVersion(appID)
		if !ok {
			s.logger.Warn("unsupported launcher patch AppId", "app_id", appID, "client_version", clientVersion)
			writeFetchError(writer, fmt.Sprintf("unsupported AppId %d", appID))
			return
		}

		patches, _, supported := s.catalog.PatchesAfter(appID, clientVersion)
		s.logger.Info("launcher patch request", "app_id", appID, "client_version", clientVersion, "latest_version", latestVersion, "patches", len(patches),
			"host", request.Host, "remote", request.RemoteAddr)
		switch {
		case clientVersion > latestVersion:
			writeFetchError(writer, fmt.Sprintf("client version %d is newer than server version %d", clientVersion, latestVersion))
		case !supported:
			writeFetchError(writer, fmt.Sprintf("patch records unavailable for version %d", clientVersion))
		case len(patches) > 0:
			var manifest strings.Builder
			if err := s.writePatchManifest(&manifest, request, patches, latestVersion); err != nil {
				s.logger.Warn("cannot build launcher patch manifest", "app_id", appID, "client_version", clientVersion, "error", err)
				writeFetchError(writer, "invalid public patch URL")
			} else {
				_, _ = io.WriteString(writer, manifest.String())
			}
		default:
			_, _ = fmt.Fprintf(writer, "~0;0;%d\r\n+Up to date\r\n", latestVersion)
		}
	}
}

func (s *Server) writePatchManifest(writer io.Writer, request *http.Request, patches []Patch, latestVersion int) error {
	var totalSize int64
	for _, patch := range patches {
		totalSize += patch.Size
	}
	if totalSize > int64(^uint32(0)>>1) {
		return fmt.Errorf("selected patches total %d bytes", totalSize)
	}
	if _, err := fmt.Fprintf(writer, "~%d;%d;%d\r\n", len(patches), totalSize, latestVersion); err != nil {
		return err
	}
	for _, patch := range patches {
		downloadURL, err := s.downloadURL(request, patch)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(
			writer, "M;%s;%s;%d;%d\r\n",
			patch.TargetArchive, downloadURL, patch.XFSGeneration, patch.Size,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) downloadURL(request *http.Request, patch Patch) (string, error) {
	var base url.URL
	if s.publicURL != nil {
		base = *s.publicURL
	} else {
		if request.Host == "" || strings.ContainsAny(request.Host, ";\r\n") {
			return "", fmt.Errorf("invalid request host %q", request.Host)
		}
		base = url.URL{Scheme: "http", Host: request.Host}
		if request.TLS != nil {
			base.Scheme = "https"
		}
	}
	base.Path = path.Join(base.Path, "patches", patch.FileName)
	// Patch filenames include their target generation, providing a distinct URL
	// for each release without a cache-busting query parameter.
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func (s *Server) patchHandler(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(request.URL.Path, "/patches/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(writer, request)
		return
	}
	patch, ok := s.catalog.PatchByName(name)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	file, err := os.Open(patch.path)
	if err != nil {
		s.logger.Error("open catalog patch", "file", patch.FileName, "error", err)
		http.Error(writer, "patch unavailable", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != patch.Size {
		s.logger.Error("catalog patch changed after startup", "file", patch.FileName, "error", err)
		http.Error(writer, "patch unavailable", http.StatusServiceUnavailable)
		return
	}
	// WinInet's legacy resume path sends Range, which ServeContent supports, but
	// it does not reliably recover from cached 304/empty responses. Always make
	// the selected bytes available and explicitly disable response caching.
	request.Header.Del("If-Modified-Since")
	request.Header.Del("If-None-Match")
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", patch.FileName))
	writer.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("Expires", "0")
	logged := &patchResponseWriter{ResponseWriter: writer}
	http.ServeContent(logged, request, patch.FileName, info.ModTime(), file)
	status := logged.status
	if status == 0 {
		status = http.StatusOK
	}
	s.logger.Info("launcher patch download", "file", patch.FileName, "method", request.Method,
		"status", status, "bytes", logged.bytes, "range", request.Header.Get("Range"),
		"remote", request.RemoteAddr, "user_agent", request.UserAgent())
}

type patchResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (writer *patchResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *patchResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	count, err := writer.ResponseWriter.Write(data)
	writer.bytes += count
	return count, err
}

func parseFetchQuery(rawQuery string) (appID, version int, err error) {
	parts := strings.Split(rawQuery, "&")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, fmt.Errorf("expected AppId&Version")
	}
	appID, err = strconv.Atoi(parts[0])
	if err != nil || appID < 1 {
		return 0, 0, fmt.Errorf("invalid AppId %q", parts[0])
	}
	version, err = strconv.Atoi(parts[1])
	if err != nil || version < 0 {
		return 0, 0, fmt.Errorf("invalid Version %q", parts[1])
	}
	return appID, version, nil
}

func writeFetchError(writer io.Writer, message string) {
	_, _ = fmt.Fprintf(writer, "Syntax Error: %s\r\n", message)
}

func launcherPage(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(writer, "<html><body></body></html>")
}

func inspectionTime(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = io.WriteString(writer, "<InspTime><GAMENO>2</GAMENO><StartTime></StartTime><EndTime></EndTime></InspTime>")
}

func health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(writer, "ok")
}
