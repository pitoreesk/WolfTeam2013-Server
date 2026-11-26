package patcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogDerivesLatestVersionsFromNames(t *testing.T) {
	directory := t.TempDir()
	writeTestXFS(t, filepath.Join(directory, "launcher_21.xfs"), 2, 1)
	writeTestXFS(t, filepath.Join(directory, "wolf_378.xfs"), 1, 378)
	writeTestXFS(t, filepath.Join(directory, "wolf_379.xfs"), 1, 379)
	catalog, err := LoadCatalog(directory, 20, 377)
	if err != nil {
		t.Fatal(err)
	}
	if latest, _ := catalog.LatestVersion(launcherAppID); latest != 21 {
		t.Fatalf("launcher latest = %d", latest)
	}
	if latest, _ := catalog.LatestVersion(gameAppID); latest != 379 {
		t.Fatalf("game latest = %d", latest)
	}
	patches, latest, ok := catalog.PatchesAfter(gameAppID, 378)
	if !ok || latest != 379 || len(patches) != 1 || patches[0].FileName != "wolf_379.xfs" {
		t.Fatalf("patch selection = %+v, latest %d, supported %t", patches, latest, ok)
	}
}

func TestCatalogRejectsVersionGaps(t *testing.T) {
	directory := t.TempDir()
	writeTestXFS(t, filepath.Join(directory, "wolf_379.xfs"), 1, 379)
	_, err := LoadCatalog(directory, 20, 377)
	if err == nil || !strings.Contains(err.Error(), "needs version 378") {
		t.Fatalf("gap error = %v", err)
	}
}

func TestCatalogRejectsAmbiguousXFSNames(t *testing.T) {
	directory := t.TempDir()
	writeTestXFS(t, filepath.Join(directory, "wolf_377_378.xfs"), 1, 378)
	_, err := LoadCatalog(directory, 20, 377)
	if err == nil || !strings.Contains(err.Error(), "unrecognized patch filename") {
		t.Fatalf("name error = %v", err)
	}
}

func TestCatalogRejectsWrongXFSFormat(t *testing.T) {
	directory := t.TempDir()
	writeTestXFS(t, filepath.Join(directory, "launcher_21.xfs"), 1, 1)
	_, err := LoadCatalog(directory, 20, 377)
	if err == nil || !strings.Contains(err.Error(), "expected XFS2") {
		t.Fatalf("format error = %v", err)
	}
}

func TestCatalogRejectsWrongGameGeneration(t *testing.T) {
	directory := t.TempDir()
	writeTestXFS(t, filepath.Join(directory, "wolf_378.xfs"), 1, 379)
	_, err := LoadCatalog(directory, 20, 377)
	if err == nil || !strings.Contains(err.Error(), "expected 378") {
		t.Fatalf("generation error = %v", err)
	}
}

func TestCatalogRejectsChangedPatchSize(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "wolf_378.xfs")
	writeTestXFS(t, path, 1, 378)
	catalog, err := LoadCatalog(directory, 20, 377)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, ok := catalog.PatchByName("wolf_378.xfs")
	if !ok || patch.Size == int64(len("changed")) {
		t.Fatalf("catalog did not retain startup metadata: %+v", patch)
	}
}
