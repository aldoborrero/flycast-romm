package flycast

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrOutsideRoot = errors.New("rom_path must be within ROM_ROOT")
	ErrNotFound    = errors.New("rom_path does not exist")
	ErrNoBootable  = errors.New("no bootable ROM file found under rom_path")
)

// Extensions are the formats Flycast can boot, best first. A folder holding
// several candidates picks by this order, so a .chd beats a .gdi beside it and
// a real disc image beats a homebrew .elf dropped in the same folder.
//
// Verified against Flycast's own handling: core/cfg/cl.cpp treats .cdi, .chd,
// .gdi and .cue as CD images and .elf as a reios binary, and
// core/ui/game_scanner.cpp accepts .zip and .7z (NAOMI and Atomiswave carts).
// .bin is deliberately absent: it is a CUE track, never a boot target.
var Extensions = []string{".chd", ".gdi", ".cdi", ".cue", ".zip", ".7z", ".elf"}

// playlistExt is a multi-disc playlist. The standalone Flycast the broker
// drives has no playlist loader (only its libretro core does), so .m3u is
// deliberately absent from Extensions: a .m3u is resolved to the first disc it
// names rather than handed to the emulator, which would reject it as an unknown
// disk format. Switching discs is a runtime action inside Flycast, not
// something the .m3u drives.
const playlistExt = ".m3u"

// searchGlobs bound how deep a folder-organised game is searched: the folder
// itself, then one level down for the per-disc subfolders some sets use
// (Game/Disc 1/game.gdi). Nothing deeper — a launch must not pay for a full
// walk of a large set, and anything further down is extras, not the game.
var searchGlobs = []string{"*", "*/*"}

// "Disc 1", "(Disc 2)", "CD1", "Disk_3" anywhere in a folder or file name.
var discRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:disc|disk|cd)[\s._-]*(\d+)`)

// ResolveROM turns the path RomM sent into a file Flycast can boot.
//
// RomM addresses a multi-file ROM by its folder, because Rom.full_path is
// fs_path/fs_name and for a multi-file ROM fs_name *is* the directory. A
// library laid out one game per folder therefore sends a path Flycast cannot
// boot, and the disc image has to be found inside it. A .m3u playlist arrives
// as a plain file and is just as unbootable, so it resolves to the first disc
// it names.
func ResolveROM(root, raw string) (string, error) {
	return resolveROM(root, raw, 0)
}

// resolveROM is ResolveROM with a recursion depth. depth > 0 means we arrived
// from a .m3u entry; a second playlist reached at that point is refused rather
// than followed. Because this check runs after EvalSymlinks below, it bounds
// the recursion on the symlink-resolved path — a disc entry that is a symlink
// back to a playlist is caught here, which a check on the literal text is not.
func resolveROM(root, raw string, depth int) (string, error) {
	path, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, raw)
	}
	path = filepath.Clean(path)

	// Resolve symlinks before the containment check so a link out of the
	// library cannot be used to read arbitrary files. A path that does not
	// exist yet resolves to itself, and is caught by the Stat below.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if !underRoot(root, path) {
		return "", ErrOutsideRoot
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", ErrNotFound
	}
	if !info.IsDir() {
		if isPlaylist(path) {
			if depth > 0 {
				return "", fmt.Errorf("%w: playlist references another playlist", ErrNoBootable)
			}
			return resolvePlaylist(root, path, depth)
		}
		if !supported(path) {
			return "", fmt.Errorf("%w: unsupported extension %q", ErrNoBootable, filepath.Ext(path))
		}
		return path, nil
	}

	best := pickROM(root, path)
	if best == "" {
		return "", ErrNoBootable
	}
	return best, nil
}

func isPlaylist(path string) bool {
	return strings.EqualFold(filepath.Ext(path), playlistExt)
}

// resolvePlaylist reads a .m3u and returns the first disc it names, resolved
// relative to the playlist's own directory. The disc goes back through
// resolveROM so it inherits the same containment and symlink checks as any
// direct launch, and the depth bound there refuses a playlist that resolves to
// another playlist rather than following it.
func resolvePlaylist(root, m3u string, depth int) (string, error) {
	f, err := os.Open(m3u)
	if err != nil {
		return "", ErrNotFound
	}
	defer f.Close()

	entry := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		// Strip a UTF-8 BOM (editors leave one on the first line) and the
		// surrounding quotes some playlists wrap paths in, as Flycast's own
		// m3u reader does.
		line := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "\ufeff")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry = strings.Trim(line, `"`)
		break
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("%w: unreadable playlist: %v", ErrNoBootable, err)
	}
	if entry == "" {
		return "", ErrNoBootable
	}

	disc := entry
	if !filepath.IsAbs(disc) {
		disc = filepath.Join(filepath.Dir(m3u), disc)
	}
	resolved, err := resolveROM(root, disc, depth+1)
	if err != nil {
		// Name the disc, not the playlist: the .m3u exists, its entry is what
		// failed to resolve. The error class is preserved so the handler still
		// maps it the same way.
		return "", fmt.Errorf("playlist entry %q: %w", entry, err)
	}
	return resolved, nil
}

type candidate struct {
	path string
	rank int // index into Extensions
	disc int
}

func pickROM(root, dir string) string {
	var found []candidate
	seen := map[string]bool{}

	for _, glob := range searchGlobs {
		matches, err := filepath.Glob(filepath.Join(dir, glob))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if seen[m] {
				continue
			}
			seen[m] = true

			rel, err := filepath.Rel(dir, m)
			if err != nil || strings.HasPrefix(filepath.Base(m), ".") {
				continue
			}
			rank := extRank(m)
			if rank < 0 {
				continue
			}
			// A symlink inside the folder must not be a way out of the
			// library either.
			real, err := filepath.EvalSymlinks(m)
			if err != nil || !underRoot(root, real) {
				continue
			}
			info, err := os.Stat(real)
			if err != nil || info.IsDir() {
				continue
			}
			found = append(found, candidate{path: m, rank: rank, disc: discNumber(rel)})
		}
	}
	if len(found) == 0 {
		return ""
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].rank != found[j].rank {
			return found[i].rank < found[j].rank
		}
		if found[i].disc != found[j].disc {
			return found[i].disc < found[j].disc
		}
		return found[i].path < found[j].path
	})
	return found[0].path
}

// discNumber extracts the disc number from a name so a multi-disc set boots
// disc 1. Names with no disc marker sort first, as a single-disc game should.
func discNumber(name string) int {
	m := discRe.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

func extRank(path string) int {
	ext := strings.ToLower(filepath.Ext(path))
	for i, e := range Extensions {
		if ext == e {
			return i
		}
	}
	return -1
}

func supported(path string) bool { return extRank(path) >= 0 }

func underRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
