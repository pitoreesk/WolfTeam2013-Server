package patcher

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ClientFile struct {
	Path            string `json:"path"`
	URL             string `json:"url"`
	Size            int64  `json:"size"`
	SHA256          string `json:"sha256"`
	path            string
	storedSize      int64
	contentEncoding string
}

type ClientManifest struct {
	Schema  int          `json:"schema"`
	Version int          `json:"version"`
	Files   []ClientFile `json:"files"`
}

type ClientCatalog struct {
	manifest ClientManifest
	byPath   map[string]ClientFile
}

func LoadClientCatalog(directory string, version int) (*ClientCatalog, error) {
	catalog := &ClientCatalog{
		manifest: ClientManifest{Schema: 1, Version: version, Files: []ClientFile{}},
		byPath:   make(map[string]ClientFile),
	}
	info, err := os.Stat(directory)
	if os.IsNotExist(err) {
		return catalog, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect %q: %w", directory, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", directory)
	}

	err = filepath.WalkDir(directory, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == directory {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symbolic link", filePath)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", filePath)
		}
		relative, err := filepath.Rel(directory, filePath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." || strings.HasPrefix(relative, "../") || strings.Contains(relative, "\\") {
			return fmt.Errorf("unsafe client file path %q", relative)
		}
		logicalPath := relative
		contentEncoding := ""
		var size int64
		var hash string
		if strings.HasSuffix(strings.ToLower(relative), ".gz") {
			logicalPath = relative[:len(relative)-len(".gz")]
			if logicalPath == "" {
				return fmt.Errorf("compressed client file %q has an empty logical path", relative)
			}
			size, hash, err = hashGzipClientFile(filePath)
			contentEncoding = "gzip"
		} else {
			size = info.Size()
			hash, err = hashClientFile(filePath)
		}
		if err != nil {
			return fmt.Errorf("inspect client payload %q: %w", filePath, err)
		}
		key := strings.ToLower(logicalPath)
		if existing, ok := catalog.byPath[key]; ok {
			return fmt.Errorf(
				"client payloads %q and %q target the same Windows path %q",
				existing.path, filePath, logicalPath,
			)
		}
		file := ClientFile{
			Path: logicalPath, URL: "/client-files/" + escapeURLPath(logicalPath),
			Size: size, SHA256: hash, path: filePath,
			storedSize: info.Size(), contentEncoding: contentEncoding,
		}
		catalog.manifest.Files = append(catalog.manifest.Files, file)
		catalog.byPath[key] = file
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(catalog.manifest.Files, func(i, j int) bool {
		return catalog.manifest.Files[i].Path < catalog.manifest.Files[j].Path
	})
	return catalog, nil
}

// clientCatalogState cheaply detects changes in the mounted-file directory.
// Full content hashes are calculated only after this metadata signature
// changes.
func clientCatalogState(directory string) (string, error) {
	info, err := os.Stat(directory)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %q: %w", directory, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", directory)
	}

	var records []string
	err = filepath.WalkDir(directory, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == directory {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symbolic link", filePath)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", filePath)
		}
		relative, err := filepath.Rel(directory, filePath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." || strings.HasPrefix(relative, "../") || strings.Contains(relative, "\\") {
			return fmt.Errorf("unsafe client file path %q", relative)
		}
		records = append(records, fmt.Sprintf(
			"%s\x00%d\x00%d\x00%o",
			relative, info.Size(), info.ModTime().UnixNano(), info.Mode().Perm(),
		))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(records)
	hash := sha256.New()
	for _, record := range records {
		_, _ = io.WriteString(hash, record)
		_, _ = io.WriteString(hash, "\n")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashClientFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashGzipClientFile(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, reader)
	closeErr := reader.Close()
	if copyErr != nil {
		return 0, "", copyErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func escapeURLPath(value string) string {
	parts := strings.Split(value, "/")
	for index, part := range parts {
		parts[index] = urlPathEscape(part)
	}
	return strings.Join(parts, "/")
}
