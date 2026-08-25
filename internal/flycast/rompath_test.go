package flycast

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveSingleFile(t *testing.T) {
	root := t.TempDir()
	rom := write(t, filepath.Join(root, "dc", "Crazy Taxi.chd"))

	got, err := ResolveROM(root, rom)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if got != rom {
		t.Fatalf("ResolveROM = %q, want %q", got, rom)
	}
}

// RomM addresses a multi-file ROM by its folder, because Rom.full_path is
// fs_path/fs_name and fs_name is the directory. Flycast cannot boot a folder.
func TestResolveFolderPicksTheDiscImage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc", "Skies of Arcadia")
	write(t, filepath.Join(dir, "Skies of Arcadia.gdi"))
	write(t, filepath.Join(dir, "track01.bin"))
	write(t, filepath.Join(dir, "track02.raw"))

	got, err := ResolveROM(root, dir)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "Skies of Arcadia.gdi" {
		t.Fatalf("ResolveROM = %q, want the .gdi", got)
	}
}

// .bin is a CUE track, never a boot target: a folder holding only tracks has
// nothing bootable in it.
func TestResolveRejectsTrackFilesAlone(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc", "Tracks Only")
	write(t, filepath.Join(dir, "track01.bin"))

	_, err := ResolveROM(root, dir)
	if !errors.Is(err, ErrNoBootable) {
		t.Fatalf("ResolveROM = %v, want ErrNoBootable", err)
	}
}

func TestResolvePrefersByFormatThenDisc(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc", "Shenmue")
	write(t, filepath.Join(dir, "Shenmue (Disc 2).gdi"))
	write(t, filepath.Join(dir, "Shenmue (Disc 1).gdi"))
	write(t, filepath.Join(dir, "Shenmue.cdi"))

	got, err := ResolveROM(root, dir)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	// .gdi outranks .cdi, and within .gdi disc 1 comes first.
	if filepath.Base(got) != "Shenmue (Disc 1).gdi" {
		t.Fatalf("ResolveROM = %q, want disc 1", got)
	}
}

// Some sets put each disc in its own subfolder. One level down, no deeper.
func TestResolveLooksOneLevelDown(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc", "Grandia II")
	write(t, filepath.Join(dir, "Disc 1", "game.gdi"))

	got, err := ResolveROM(root, dir)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "game.gdi" {
		t.Fatalf("ResolveROM = %q, want the nested .gdi", got)
	}

	deeper := filepath.Join(root, "dc", "Too Deep")
	write(t, filepath.Join(deeper, "a", "b", "game.gdi"))
	if _, err := ResolveROM(root, deeper); !errors.Is(err, ErrNoBootable) {
		t.Fatalf("ResolveROM two levels down = %v, want ErrNoBootable", err)
	}
}

func TestResolveRejectsPathsOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := write(t, filepath.Join(t.TempDir(), "elsewhere.chd"))

	for _, raw := range []string{outside, filepath.Join(root, "..", "elsewhere.chd")} {
		if _, err := ResolveROM(root, raw); !errors.Is(err, ErrOutsideRoot) && !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveROM(%q) = %v, want a containment or not-found refusal", raw, err)
		}
	}
}

// A symlink out of the library must not become a way to read arbitrary files.
func TestResolveRejectsEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	target := write(t, filepath.Join(t.TempDir(), "secret.chd"))
	link := filepath.Join(root, "dc", "link.chd")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := ResolveROM(root, link); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("ResolveROM through an escaping symlink = %v, want ErrOutsideRoot", err)
	}
}

func TestResolveSkipsDotFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc", "Dots")
	write(t, filepath.Join(dir, "._resource.chd"))
	write(t, filepath.Join(dir, "real.chd"))

	got, err := ResolveROM(root, dir)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "real.chd" {
		t.Fatalf("ResolveROM = %q, want real.chd", got)
	}
}

func TestResolveMissingPath(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveROM(root, filepath.Join(root, "dc", "nope.chd")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveROM of a missing file = %v, want ErrNotFound", err)
	}
}

// NAOMI and Atomiswave carts are zip/7z sets, which Flycast accepts as a "rom"
// rather than a CD image.
func TestResolveAcceptsArcadeCarts(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"mvsc2.zip", "fotns.7z"} {
		rom := write(t, filepath.Join(root, "naomi", name))
		if _, err := ResolveROM(root, rom); err != nil {
			t.Fatalf("ResolveROM(%s): %v", name, err)
		}
	}
}
