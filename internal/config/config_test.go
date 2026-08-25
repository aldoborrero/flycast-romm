package config

import (
	"testing"
	"time"
)

// RomM slots are 1-based and Flycast's SavestateSlot is 0-based, so every slot
// crossing this boundary is one off. Getting it wrong writes slot N and reads
// slot N+1, which looks like save states silently not working.
func TestFlycastSlotMapping(t *testing.T) {
	c := Config{SaveSlot: MaxSlot}

	for romm, want := range map[int]int{1: 0, 2: 1, 9: 8, 10: 9} {
		if got := c.FlycastSlot(romm); got != want {
			t.Errorf("FlycastSlot(%d) = %d, want %d", romm, got, want)
		}
	}

	// Slot 0 is RomM's "use the default slot" alias, sent by /save-and-exit.
	if got := c.FlycastSlot(0); got != MaxSlot-1 {
		t.Errorf("FlycastSlot(0) = %d, want the autosave slot %d", got, MaxSlot-1)
	}

	// An operator who moves the autosave slot moves where slot 0 lands.
	if got := (Config{SaveSlot: 5}).FlycastSlot(0); got != 4 {
		t.Errorf("FlycastSlot(0) with SAVE_SLOT=5 = %d, want 4", got)
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 8000 {
		t.Errorf("Port = %d, want the 8000 RomM assumes when broker_host omits one", c.Port)
	}
	if c.ROMRoot != "/romm/library" {
		t.Errorf("ROMRoot = %q, want RomM's LIBRARY_BASE_PATH default", c.ROMRoot)
	}
	if c.SaveSlot != MaxSlot {
		t.Errorf("SaveSlot = %d, want %d", c.SaveSlot, MaxSlot)
	}
	// The launch wait has to stay inside RomM's fixed 10s /launch timeout.
	if c.LaunchWait >= 10*time.Second {
		t.Errorf("LaunchWait = %s, which can outlast RomM's 10s launch timeout", c.LaunchWait)
	}
	if c.StateDir != c.DataDir {
		t.Errorf("StateDir = %q, want it to default to the data dir %q", c.StateDir, c.DataDir)
	}
}

// RomM's own variable is STREAMING_BROKER_SECRET; the sibling brokers call the
// container-side one BROKER_SECRET. Both work so an operator can reuse either.
func TestSecretAcceptsEitherName(t *testing.T) {
	t.Setenv("STREAMING_BROKER_SECRET", "from-romm")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Secret != "from-romm" {
		t.Fatalf("Secret = %q, want the STREAMING_BROKER_SECRET value", c.Secret)
	}

	t.Setenv("BROKER_SECRET", "explicit")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Secret != "explicit" {
		t.Fatalf("Secret = %q, want BROKER_SECRET to win", c.Secret)
	}
}

// The Python brokers document their waits as bare seconds; Go durations are
// nicer. Both are accepted rather than one silently falling back to a default.
func TestDurationsAcceptSecondsAndGoSyntax(t *testing.T) {
	t.Setenv("SAVE_WAIT", "12")
	t.Setenv("LUA_WAIT", "1m30s")
	t.Setenv("QUIT_WAIT", "nonsense")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SaveWait != 12*time.Second {
		t.Errorf("SAVE_WAIT=12 gave %s, want 12s", c.SaveWait)
	}
	if c.LuaWait != 90*time.Second {
		t.Errorf("LUA_WAIT=1m30s gave %s, want 1m30s", c.LuaWait)
	}
	if c.QuitWait != 6*time.Second {
		t.Errorf("an unparseable QUIT_WAIT gave %s, want the default", c.QuitWait)
	}
}

func TestLoadRejectsAnImpossibleSlot(t *testing.T) {
	t.Setenv("SAVE_SLOT", "11")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted SAVE_SLOT=11, which Flycast has no room for")
	}
}

func TestChannelAndScriptPaths(t *testing.T) {
	t.Setenv("FLYCAST_CONFIG_DIR", "/somewhere/flycast")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.ChannelDir(), "/somewhere/flycast/"+ChannelDirName; got != want {
		t.Errorf("ChannelDir = %q, want %q", got, want)
	}
	// Not flycast.lua: taking the default name would disable a script the
	// user wrote themselves.
	if got, want := c.LuaScriptPath(), "/somewhere/flycast/"+LuaScriptName; got != want {
		t.Errorf("LuaScriptPath = %q, want %q", got, want)
	}
	if LuaScriptName == "flycast.lua" {
		t.Error("the mod's script must not take Flycast's default script name")
	}
}
