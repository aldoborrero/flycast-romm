package flycast

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// getSavestatePath appends no suffix at all for index 0, so RomM slot 1 is
// `<basename>.state`. Getting this wrong silently writes and reads different
// files.
func TestStateFileName(t *testing.T) {
	for _, tc := range []struct {
		rom  string
		slot int
		want string
	}{
		{"/romm/library/dc/Crazy Taxi.chd", 0, "Crazy Taxi.state"},
		{"/romm/library/dc/Crazy Taxi.chd", 1, "Crazy Taxi_1.state"},
		{"/romm/library/dc/Crazy Taxi.chd", 9, "Crazy Taxi_9.state"},
		{"/romm/library/naomi/mvsc2.zip", 4, "mvsc2_4.state"},
		{"game.no.dots.gdi", 2, "game.no.dots_2.state"},
	} {
		if got := StateFileName(tc.rom, tc.slot); got != tc.want {
			t.Errorf("StateFileName(%q, %d) = %q, want %q", tc.rom, tc.slot, got, tc.want)
		}
	}
}

func TestWaitForStateWriteDetectsANewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "game.state")
	before := stamp(path)

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(path, make([]byte, 1024), 0o644)
	}()

	if !waitForStateWrite(context.Background(), path, before, 3*time.Second) {
		t.Fatal("waitForStateWrite did not see the new state file")
	}
}

// The point of the wait is that a still-growing file is not treated as done:
// killing the emulator there truncates the state.
func TestWaitForStateWriteWaitsForTheSizeToSettle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "game.state")
	before := stamp(path)

	done := make(chan struct{})
	go func() {
		defer close(done)
		f, err := os.Create(path)
		if err != nil {
			return
		}
		defer f.Close()
		for range 8 {
			_, _ = f.Write(make([]byte, 4096))
			_ = f.Sync()
			time.Sleep(60 * time.Millisecond)
		}
	}()

	start := time.Now()
	if !waitForStateWrite(context.Background(), path, before, 5*time.Second) {
		t.Fatal("waitForStateWrite gave up on a file that was still being written")
	}
	<-done
	if time.Since(start) < 400*time.Millisecond {
		t.Fatalf("waitForStateWrite returned after %s, before the writes could settle", time.Since(start))
	}
}

// An overwrite of an existing slot changes mtime, not necessarily size.
func TestWaitForStateWriteDetectsAnOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "game.state")
	if err := os.WriteFile(path, make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before := stamp(path)

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(path, make([]byte, 512), 0o644)
	}()

	if !waitForStateWrite(context.Background(), path, before, 3*time.Second) {
		t.Fatal("waitForStateWrite missed a same-size overwrite")
	}
}

func TestWaitForStateWriteGivesUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "never.state")

	if waitForStateWrite(context.Background(), path, stamp(path), 200*time.Millisecond) {
		t.Fatal("waitForStateWrite reported a write that never happened")
	}
}
