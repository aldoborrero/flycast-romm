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

// A .m3u playlist is its own ROM in a loose-file library, and RomM sends its
// path directly. Flycast cannot boot a playlist, so the broker resolves it to
// the first disc the playlist names.
func TestResolvePlaylistBootsFirstDisc(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	write(t, filepath.Join(dir, "Shenmue (Disc 1).chd"))
	write(t, filepath.Join(dir, "Shenmue (Disc 2).chd"))
	write(t, filepath.Join(dir, "Shenmue (Disc 3).chd"))
	m3u := filepath.Join(dir, "Shenmue.m3u")
	if err := os.WriteFile(m3u, []byte("Shenmue (Disc 1).chd\nShenmue (Disc 2).chd\nShenmue (Disc 3).chd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveROM(root, m3u)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "Shenmue (Disc 1).chd" {
		t.Fatalf("ResolveROM = %q, want the first disc", got)
	}
}

// A Windows-authored playlist has CRLF line endings and may carry blank lines
// or #EXTM3U-style comments before the first disc.
func TestResolvePlaylistSkipsCommentsBlanksAndCR(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	write(t, filepath.Join(dir, "D2 (Disc 1).chd"))
	m3u := filepath.Join(dir, "D2.m3u")
	body := "#EXTM3U\r\n\r\nD2 (Disc 1).chd\r\n"
	if err := os.WriteFile(m3u, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveROM(root, m3u)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "D2 (Disc 1).chd" {
		t.Fatalf("ResolveROM = %q, want the first disc", got)
	}
}

// The disc a playlist names is resolved through ResolveROM again, so an entry
// that escapes the library is refused exactly like a direct launch would be.
func TestResolvePlaylistRejectsEscapingEntry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	m3u := filepath.Join(dir, "evil.m3u")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m3u, []byte("../../../../etc/passwd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveROM(root, m3u); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("ResolveROM = %v, want ErrOutsideRoot", err)
	}
}

func TestResolvePlaylistMissingDisc(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	m3u := filepath.Join(dir, "gone.m3u")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m3u, []byte("Gone (Disc 1).chd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveROM(root, m3u); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveROM = %v, want ErrNotFound", err)
	}
}

func TestResolvePlaylistEmpty(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	m3u := filepath.Join(dir, "empty.m3u")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m3u, []byte("# only a comment\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveROM(root, m3u); !errors.Is(err, ErrNoBootable) {
		t.Fatalf("ResolveROM = %v, want ErrNoBootable", err)
	}
}

// A playlist naming another playlist is refused, not followed: Flycast cannot
// boot either and following it risks looping.
func TestResolvePlaylistRejectsNestedPlaylist(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "Game.chd"))
	if err := os.WriteFile(filepath.Join(dir, "inner.m3u"), []byte("Game.chd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outer := filepath.Join(dir, "outer.m3u")
	if err := os.WriteFile(outer, []byte("inner.m3u\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveROM(root, outer); !errors.Is(err, ErrNoBootable) {
		t.Fatalf("ResolveROM = %v, want ErrNoBootable", err)
	}
}

// An editor-authored playlist can carry a UTF-8 BOM on its first line, which
// is not whitespace and would otherwise stick to the first disc's name.
func TestResolvePlaylistStripsBOM(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	write(t, filepath.Join(dir, "D2 (Disc 1).chd"))
	m3u := filepath.Join(dir, "D2.m3u")
	if err := os.WriteFile(m3u, []byte("\ufeffD2 (Disc 1).chd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveROM(root, m3u)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "D2 (Disc 1).chd" {
		t.Fatalf("ResolveROM = %q, want the first disc", got)
	}
}

// The playlist decides the boot disc by order: the first entry wins even when
// it is not disc 1. (A folder, by contrast, sorts by disc number.)
func TestResolvePlaylistFirstEntryWins(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	write(t, filepath.Join(dir, "Game (Disc 1).chd"))
	write(t, filepath.Join(dir, "Game (Disc 2).chd"))
	m3u := filepath.Join(dir, "Game.m3u")
	if err := os.WriteFile(m3u, []byte("Game (Disc 2).chd\nGame (Disc 1).chd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveROM(root, m3u)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "Game (Disc 2).chd" {
		t.Fatalf("ResolveROM = %q, want the first listed disc", got)
	}
}

// An absolute entry that stays within the library is accepted (the escaping
// case is covered separately).
func TestResolvePlaylistAbsoluteEntryWithinRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	disc := write(t, filepath.Join(dir, "Game.chd"))
	m3u := filepath.Join(dir, "Game.m3u")
	if err := os.WriteFile(m3u, []byte(disc+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveROM(root, m3u)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "Game.chd" {
		t.Fatalf("ResolveROM = %q, want the disc", got)
	}
}

// A relative entry may point into a subdirectory beside the playlist.
func TestResolvePlaylistSubdirEntry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	write(t, filepath.Join(dir, "discs", "Game (Disc 1).chd"))
	m3u := filepath.Join(dir, "Game.m3u")
	if err := os.WriteFile(m3u, []byte("discs/Game (Disc 1).chd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveROM(root, m3u)
	if err != nil {
		t.Fatalf("ResolveROM: %v", err)
	}
	if filepath.Base(got) != "Game (Disc 1).chd" {
		t.Fatalf("ResolveROM = %q, want the nested disc", got)
	}
}

// A disc entry that is a symlink back to a playlist must not re-enter playlist
// resolution: the loop bound is on the symlink-resolved path, not the literal
// text, so it cannot be evaded with an innocent-looking .chd name. Without the
// bound this recurses until it exhausts the process's file descriptors.
func TestResolvePlaylistRejectsSymlinkLoop(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m3u := filepath.Join(dir, "outer.m3u")
	if err := os.WriteFile(m3u, []byte("self.chd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(m3u, filepath.Join(dir, "self.chd")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := ResolveROM(root, m3u); !errors.Is(err, ErrNoBootable) {
		t.Fatalf("ResolveROM = %v, want ErrNoBootable", err)
	}
}
