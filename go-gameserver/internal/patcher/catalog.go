package patcher

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	launcherAppID = 1000
	gameAppID     = 2000
)

var patchNamePattern = regexp.MustCompile(`^(launcher|wolf)_([1-9][0-9]*)\.xfs$`)

type Patch struct {
	AppID         int
	Version       int
	FileName      string
	TargetArchive string
	Size          int64
	XFSVersion    int
	XFSGeneration uint32
	path          string
}

type Catalog struct {
	baselines map[int]int
	patches   map[int][]Patch
	byName    map[string]Patch
}

func LoadCatalog(directory string, launcherBaseline, gameBaseline int) (*Catalog, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read patch directory %q: %w", directory, err)
	}
	catalog := &Catalog{
		baselines: map[int]int{launcherAppID: launcherBaseline, gameAppID: gameBaseline},
		patches:   map[int][]Patch{launcherAppID: {}, gameAppID: {}},
		byName:    make(map[string]Patch),
	}
	for _, entry := range entries {
		name := entry.Name()
		match := patchNamePattern.FindStringSubmatch(name)
		if match == nil {
			if strings.EqualFold(filepath.Ext(name), ".xfs") {
				return nil, fmt.Errorf("unrecognized patch filename %q; expected launcher_<version>.xfs or wolf_<version>.xfs", name)
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect patch %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("patch %q is not a regular file", name)
		}
		if info.Size() < 1 || info.Size() > math.MaxInt32 {
			return nil, fmt.Errorf("patch %q size %d is outside the launcher's supported range", name, info.Size())
		}
		version64, err := strconv.ParseInt(match[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid target version in patch %q: %w", name, err)
		}
		patch := Patch{Version: int(version64), FileName: name, Size: info.Size(), path: filepath.Join(directory, name)}
		switch match[1] {
		case "launcher":
			patch.AppID = launcherAppID
			patch.TargetArchive = "LauncherEnc.xfs"
			patch.XFSVersion = 2
		case "wolf":
			patch.AppID = gameAppID
			patch.TargetArchive = "wolf.xfs"
			patch.XFSVersion = 1
		}
		actualXFSVersion, generation, err := readXFSIdentity(patch.path, patch.Size)
		if err != nil {
			return nil, fmt.Errorf("inspect patch %q: %w", name, err)
		}
		if actualXFSVersion != patch.XFSVersion {
			return nil, fmt.Errorf("patch %q is XFS%d, expected XFS%d", name, actualXFSVersion, patch.XFSVersion)
		}
		if generation == 0 {
			return nil, fmt.Errorf("patch %q has XFS generation 0", name)
		}
		if patch.AppID == gameAppID && generation != uint32(patch.Version) {
			return nil, fmt.Errorf("game patch %q has XFS generation %d, expected %d", name, generation, patch.Version)
		}
		patch.XFSGeneration = generation
		catalog.patches[patch.AppID] = append(catalog.patches[patch.AppID], patch)
		catalog.byName[name] = patch
	}

	for appID, patches := range catalog.patches {
		sort.Slice(patches, func(i, j int) bool { return patches[i].Version < patches[j].Version })
		expected := catalog.baselines[appID] + 1
		var previousGeneration uint32
		for index, patch := range patches {
			if patch.Version != expected {
				return nil, fmt.Errorf("AppId %d patch sequence needs version %d, found %d", appID, expected, patch.Version)
			}
			if index > 0 && patch.XFSGeneration <= previousGeneration {
				return nil, fmt.Errorf("AppId %d XFS generation must increase after %d, found %d in %q", appID, previousGeneration, patch.XFSGeneration, patch.FileName)
			}
			previousGeneration = patch.XFSGeneration
			expected++
		}
		catalog.patches[appID] = patches
	}
	return catalog, nil
}

func (c *Catalog) LatestVersion(appID int) (int, bool) {
	baseline, ok := c.baselines[appID]
	if !ok {
		return 0, false
	}
	patches := c.patches[appID]
	if len(patches) == 0 {
		return baseline, true
	}
	return patches[len(patches)-1].Version, true
}

func (c *Catalog) PatchesAfter(appID, clientVersion int) ([]Patch, int, bool) {
	latest, ok := c.LatestVersion(appID)
	if !ok || clientVersion < c.baselines[appID] || clientVersion > latest {
		return nil, latest, false
	}
	patches := c.patches[appID]
	index := sort.Search(len(patches), func(index int) bool { return patches[index].Version > clientVersion })
	return patches[index:], latest, true
}

func (c *Catalog) PatchByName(name string) (Patch, bool) {
	patch, ok := c.byName[name]
	return patch, ok
}

func (c *Catalog) Count() int {
	return len(c.byName)
}

func readXFSIdentity(path string, size int64) (version int, generation uint32, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	var pointer [6]byte
	pointerSize := 4
	if size > math.MaxUint32 {
		pointerSize = 6
	}
	if _, err := io.ReadFull(file, pointer[:pointerSize]); err != nil {
		return 0, 0, fmt.Errorf("read table pointer: %w", err)
	}
	tableOffset := int64(binary.LittleEndian.Uint32(pointer[:4]))
	if pointerSize == 6 {
		tableOffset |= int64(binary.LittleEndian.Uint16(pointer[4:6])) << 32
	}
	if tableOffset < int64(pointerSize) || tableOffset >= size {
		return 0, 0, fmt.Errorf("table offset %d is outside a %d-byte archive", tableOffset, size)
	}
	if _, err := file.Seek(tableOffset, io.SeekStart); err != nil {
		return 0, 0, fmt.Errorf("seek to active table: %w", err)
	}
	var compressedSize [1]byte
	if _, err := io.ReadFull(file, compressedSize[:]); err != nil {
		return 0, 0, fmt.Errorf("read compressed header size: %w", err)
	}
	if compressedSize[0] == 0 || tableOffset+1+int64(compressedSize[0]) > size {
		return 0, 0, fmt.Errorf("invalid compressed XFS header size %d", compressedSize[0])
	}
	compressed := make([]byte, compressedSize[0])
	if _, err := io.ReadFull(file, compressed); err != nil {
		return 0, 0, fmt.Errorf("read compressed XFS header: %w", err)
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return 0, 0, fmt.Errorf("open compressed XFS header: %w", err)
	}
	header, readErr := io.ReadAll(io.LimitReader(reader, 65))
	closeErr := reader.Close()
	if readErr != nil {
		return 0, 0, fmt.Errorf("decompress XFS header: %w", readErr)
	}
	if closeErr != nil {
		return 0, 0, fmt.Errorf("close XFS header stream: %w", closeErr)
	}
	if len(header) != 64 {
		return 0, 0, fmt.Errorf("XFS header decoded to %d bytes, expected 64", len(header))
	}
	switch string(header[:4]) {
	case "XFS ":
		version = 1
	case "XFS2":
		version = 2
	default:
		return 0, 0, fmt.Errorf("unknown XFS signature %q", header[:4])
	}
	generation = binary.LittleEndian.Uint32(header[4:8])
	declaredOffset := binary.LittleEndian.Uint32(header[16:20])
	if declaredOffset != uint32(tableOffset) {
		return 0, 0, fmt.Errorf("XFS header table offset %d does not match %d", declaredOffset, tableOffset)
	}
	return version, generation, nil
}
