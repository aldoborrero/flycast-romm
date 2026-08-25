package flycast

import (
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
// boot, and the disc image has to be found inside it.
func ResolveROM(root, raw string) (string, error) {
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
