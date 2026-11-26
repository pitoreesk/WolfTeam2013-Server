package patcher

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ApexWebPath is the Apex anti-cheat update root configured in the 2011
// client's avital\Config.ini: URL1=http://127.0.0.1/apex/wolfteam/psserver/web/.
const ApexWebPath = "/apex/wolfteam/psserver/web/"

// apexWebHandler serves the flat Apex update directory. The Apex downloader
// runs on Windows, where file names are case-insensitive, while the container
// file system is not, so names are matched case-insensitively. Directory
// listings and subdirectories are never served.
func apexWebHandler(dir string, logger *slog.Logger) http.HandlerFunc {
	return staticDirHandler(ApexWebPath, dir, "apex web request", logger)
}

// NoticeWebPath is where the client's Img/ServerNotice/NoticeInfo.txt (host,
// path, port) points the ServerNotice downloader (CShell sub_341C5C00): it
// fetches TextVersion.txt / NoticeVersion.txt / EventNoticeVersion.txt and
// then every listed file whose version is newer than the local copy.
const NoticeWebPath = "/softnyx/game/Notice/"

func noticeWebHandler(dir string, logger *slog.Logger) http.HandlerFunc {
	return staticDirHandler(NoticeWebPath, dir, "server notice request", logger)
}

func staticDirHandler(prefix, dir, logMessage string, logger *slog.Logger) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(request.URL.Path, prefix)
		resolved, ok := resolveApexFile(dir, name)
		logger.Info(logMessage, "path", request.URL.Path, "found", ok,
			"remote", request.RemoteAddr, "user_agent", request.UserAgent())
		if !ok {
			http.NotFound(writer, request)
			return
		}
		file, err := os.Open(resolved)
		if err != nil {
			http.NotFound(writer, request)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(writer, request, info.Name(), info.ModTime(), file)
	}
}

// resolveApexFile maps a "/"-separated request path (the 2012 web root has
// AXUP/ and pic/ subdirectories) onto dir, one case-insensitive segment at a
// time. Empty, "." and ".." segments and backslashes are rejected, so the
// result always stays inside dir, and only a regular file is returned.
func resolveApexFile(dir, name string) (string, bool) {
	if name == "" || strings.Contains(name, `\`) {
		return "", false
	}
	segments := strings.Split(name, "/")
	current := dir
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
		last := index == len(segments)-1
		entries, err := os.ReadDir(current)
		if err != nil {
			return "", false
		}
		found := false
		for _, entry := range entries {
			if !strings.EqualFold(entry.Name(), segment) {
				continue
			}
			if (last && entry.Type().IsRegular()) || (!last && entry.IsDir()) {
				current = filepath.Join(current, entry.Name())
				found = true
				break
			}
		}
		if !found {
			return "", false
		}
	}
	return current, true
}
