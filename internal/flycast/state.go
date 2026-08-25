package flycast

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StateFileName returns the savestate Flycast writes for a slot.
//
// core/oslib/oslib.cpp:getSavestatePath builds it as
// `<content basename><suffix>.state`, where suffix is empty for index 0 and
// `_<index>` otherwise, and the basename comes from settings.content.fileName
// — the ROM's filename, set in core/emulator.cpp from the resolved file's
// name. So the path is fully determined and there is no directory to scan.
//
// Note the off-by-one this creates at the RomM boundary: RomM slot 1 is
// Flycast index 0, which has *no* suffix.
func StateFileName(romPath string, flycastIndex int) string {
	base := filepath.Base(romPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if flycastIndex <= 0 {
		return base + ".state"
	}
	if flycastIndex > 99 {
		flycastIndex = 99
	}
	return base + "_" + itoa(flycastIndex) + ".state"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

type fileStamp struct {
	exists bool
	size   int64
	mtime  time.Time
}

func stamp(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, size: info.Size(), mtime: info.ModTime()}
}

// waitForStateWrite blocks until the savestate at path has been written and
// has stopped growing, or the timeout expires. It reports whether the write
// was confirmed.
//
// The Lua acknowledgement already says dc_savestate returned, but "returned"
// and "flushed" are not the same thing, and /save-and-exit kills the process
// straight afterwards. Waiting for the size to hold steady is what stops a
// large state being truncated mid-flush — the same guard the Dolphin broker
// applies for the same reason.
func waitForStateWrite(ctx context.Context, path string, before fileStamp, timeout time.Duration) bool {
	const stableFor = 500 * time.Millisecond

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var (
		lastSize    int64 = -1
		stableSince time.Time
		changed     bool
	)

	for {
		now := stamp(path)
		switch {
		case !now.exists:
			// Flycast truncates before writing on some filesystems; keep
			// waiting rather than concluding the save failed.
		case !changed:
			if !before.exists || !now.mtime.Equal(before.mtime) || now.size != before.size {
				changed = true
				lastSize = now.size
				stableSince = time.Now()
			}
		case now.size != lastSize:
			lastSize = now.size
			stableSince = time.Now()
		case time.Since(stableSince) >= stableFor:
			return true
		}

		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}
