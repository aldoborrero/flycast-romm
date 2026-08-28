# .m3u multi-disc + disc-swap in Flycast standalone — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Flycast *standalone* boot a `.m3u` multi-disc playlist to its first disc, swap discs live from the in-game menu, and reload a savestate onto the disc that was mounted when it was saved — resolving upstream issue [flyinghead/flycast#423](https://github.com/flyinghead/flycast/issues/423).

**Architecture:** A new focused unit `core/imgread/playlist.{h,cpp}` holds all pure logic: the `.m3u` parser and the savestate-sidecar encode/decode/remount helpers (fully unit-tested with googletest). The emulator gains a small playlist state + public API on `Emulator`; `loadGame` detects `.m3u` and boots disc 0; `gui.cpp` gains a disc-swap submenu; the standalone savestate functions in `nullDC.cpp` write/read a `<state>.disc` sidecar. The hot-swap engine (`Emulator::insertGdrom`) is reused unchanged. Nothing touches `libGDR_serialize` or the global savestate version, so the libretro core is unaffected.

**Tech Stack:** C++17, CMake + Ninja, googletest (git submodule `core/deps/googletest`), nix for the build toolchain. The design spec is at `docs/superpowers/specs/2026-08-28-flycast-m3u-multidisc-design.md` (read it first).

**Where the work happens:** the upstream checkout at `/home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast` (currently `flyinghead/flycast` `master`, clean). All tasks below operate in THAT repo on a feature branch. The plan document itself lives in the `flycast-romm` repo. Later the branch is pushed to the `aldoborrero/flycast` fork for the PR.

**Skills:** follow @superpowers:test-driven-development for every task that touches the pure logic (Tasks 1 and 4 — real red→green→refactor). Tasks 3, 5 and the boot wiring in Task 2 are engine/UI glue verified by build + the CT 1103 integration pass (Task 6); the spec is explicit that those are integration-verified, not unit-tested, because they need a booted machine or an ImGui frame.

---

## File Structure

| File | Responsibility | Created / Modified |
|---|---|---|
| `core/imgread/playlist.h` | Declarations: `parseM3u`, `DiscRef`, `encodeDiscSidecar`, `decodeDiscSidecar`, `resolveRemount` | Create |
| `core/imgread/playlist.cpp` | Implementations of the above (pure logic + file-less string ops; parser uses `hostfs` path helpers like `cue.cpp`) | Create |
| `core/imgread/CMakeLists.txt` | Add `playlist.cpp` / `playlist.h` to the imgread sources | Modify |
| `tests/src/imgread/M3uTest.cpp` | googletest cases for `parseM3u` and the sidecar helpers | Create |
| `tests/CMakeLists.txt:24` | Register `M3uTest.cpp` | Modify |
| `tests/files/test_m3u/…` | Fixture `.m3u` files + dummy disc files | Create |
| `core/emulator.h:104-222` | `Emulator` playlist state + public API (`discList`/`currentDisc`/`selectDisc`/`invalidateDiscIndex`) | Modify |
| `core/emulator.cpp:562` | `loadGame`: detect `.m3u`, populate playlist, boot disc 0; clear in `unloadGame:743` | Modify |
| `core/ui/gui.cpp:620,1001` | Disc-swap submenu; invalidate index on manual whole-library swap | Modify |
| `core/nullDC.cpp:181,264` | Write sidecar in `dc_savestate`; remount in `dc_loadstate` (both guarded `index >= 0`) | Modify |

---

## Task 0: F0 — establish a green test build (infra gate, NO feature code)

The spec flags this as the main infra risk: the whole emulator must compile with `-DENABLE_CTEST=ON` before any feature code is worth writing. Get the *existing* test suite green first, so later failures are unambiguously ours.

**Files:** none (build + branch only).

- [ ] **Step 1: Create the feature branch in the flycast checkout**

```bash
cd /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast
git checkout -b feat/m3u-multidisc
```

- [ ] **Step 2: Initialize the googletest submodule (required — tests won't link without it)**

```bash
git -C /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast submodule update --init core/deps/googletest
```
Expected: clones `core/deps/googletest` (~150MB, needs network). Verify `core/deps/googletest/CMakeLists.txt` now exists.

- [ ] **Step 3: Enter a nix shell with the C++ toolchain + libs**

`cmake`, `ninja`, `pkg-config` are NOT on PATH; `gcc` (15.3) and `nix` are. Use an ad-hoc nix shell (no flake needed for a local build):

```bash
nix-shell -p cmake ninja pkg-config gcc \
  SDL2 curl zlib alsa-lib libevdev udev libao \
  --run 'cmake --version && ninja --version'
```
Expected: prints cmake ≥ 3.22 and a ninja version. If a later configure/build step reports a missing dep, add the corresponding nixpkgs attr here (candidates: `libcdio`, `miniupnpc`, `lua5_4`, `libpng`, `libzip`). Keep the exact working `nix-shell -p …` invocation — every later build step runs inside it.

- [ ] **Step 4: Configure with ctest enabled, trimming optional features to shrink the dep surface and build time**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cd /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast &&
cmake -B build -G Ninja \
  -DENABLE_CTEST=ON \
  -DCMAKE_BUILD_TYPE=Release \
  -DUSE_HOST_SDL=ON \
  -DUSE_VULKAN=OFF \
  -DUSE_LUA=OFF'
```
Expected: configure succeeds and reports the googletest + tests subdirs added (`include(CTest)` auto-sets `BUILD_TESTING`, which pulls `add_subdirectory(core/deps/googletest)` and `add_subdirectory(tests)` at `CMakeLists.txt:1474-1477`). `FLYCAST_TEST_FILES` is defined to `.../tests/files` at `CMakeLists.txt:127-128`. If configure fails on a missing `find_package`, add the lib to the nix shell (Step 3) and re-run. Turning `USE_VULKAN=OFF` avoids the `glslang` dependency; `USE_LUA=OFF` avoids Lua — neither is needed by the tests.

- [ ] **Step 5: Build the whole emulator + tests (first build is slow: 15–45 min; later builds are incremental)**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cmake --build /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -j "$(nproc)"'
```
Expected: produces `build/flycast`. This is the risk moment — resolve any missing-symbol/lib errors here by adjusting the nix shell or the `-DUSE_*=OFF` flags. Do NOT proceed until it links.

- [ ] **Step 6: Run the existing test suite — must be green before writing any feature code**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
ctest --test-dir /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build --output-on-failure'
```
Expected: the existing suite (CueTest, GdiTest, …) passes. A green baseline here means the toolchain is sound. **This is the gate: if this is not green, stop and fix the build, do not start Task 1.**

- [ ] **Step 7: Commit the branch checkpoint (build artifacts are gitignored; this just records the starting point)**

No source changed yet, so there may be nothing to commit. If `git status` is clean, skip. Otherwise commit only intended files (never `build/`).

---

## Task 1: `parseM3u` — the playlist parser (pure, strict TDD)

Parse an `.m3u` into an ordered list of resolved, bootable disc paths. Pure logic, fully unit-tested. Mirrors libretro's `read_m3u` (`shell/libretro/libretro.cpp:3866`) but stricter, and resolves entries relative to the playlist directory exactly as `cue.cpp` resolves track files (`core/imgread/cue.cpp:78,181`).

**Files:**
- Create: `core/imgread/playlist.h`, `core/imgread/playlist.cpp`
- Modify: `core/imgread/CMakeLists.txt`
- Create test: `tests/src/imgread/M3uTest.cpp`
- Modify: `tests/CMakeLists.txt:24`
- Create fixtures under `tests/files/test_m3u/`

- [ ] **Step 1: Create the fixtures**

Create these files (the dummy disc files only need to exist; `parseM3u` checks extension + existence, not disc validity):

```
tests/files/test_m3u/basic.m3u          -> two lines: disc1.chd \n disc2.chd
tests/files/test_m3u/disc1.chd          -> (empty file)
tests/files/test_m3u/disc2.chd          -> (empty file)
tests/files/test_m3u/messy.m3u          -> BOM + comments + blanks + CRLF + quoted entry (see below)
tests/files/test_m3u/sub/disc3.gdi      -> (empty file)
tests/files/test_m3u/subdir.m3u         -> one line: sub/disc3.gdi
tests/files/test_m3u/escape.m3u         -> one line: ../../etc/passwd
tests/files/test_m3u/nested.m3u         -> one line: basic.m3u
tests/files/test_m3u/empty.m3u          -> (whitespace/comments only)
```

`messy.m3u` content (write the BOM bytes `EF BB BF` before the first `#`, use CRLF line endings):
```
# Shenmue
disc1.chd

"disc2.chd"
```

- [ ] **Step 2: Write the failing tests**

Create `tests/src/imgread/M3uTest.cpp`:

```cpp
#include "gtest/gtest.h"
#include "imgread/playlist.h"

class M3uTest : public ::testing::Test {};

TEST_F(M3uTest, BasicTwoDiscsResolvedRelative)
{
    std::vector<std::string> discs = parseM3u(FLYCAST_TEST_FILES "/test_m3u/basic.m3u");
    ASSERT_EQ(2u, discs.size());
    EXPECT_NE(std::string::npos, discs[0].find("disc1.chd"));
    EXPECT_NE(std::string::npos, discs[1].find("disc2.chd"));
    // resolved to an absolute/rooted path under the playlist dir, not the bare entry
    EXPECT_NE(std::string::npos, discs[0].find("test_m3u"));
}

TEST_F(M3uTest, SkipsBomCommentsBlanksAndStripsQuotesAndCRLF)
{
    std::vector<std::string> discs = parseM3u(FLYCAST_TEST_FILES "/test_m3u/messy.m3u");
    ASSERT_EQ(2u, discs.size());
    EXPECT_NE(std::string::npos, discs[0].find("disc1.chd"));
    EXPECT_NE(std::string::npos, discs[1].find("disc2.chd")); // quotes stripped
}

TEST_F(M3uTest, ResolvesSubdirectoryEntry)
{
    std::vector<std::string> discs = parseM3u(FLYCAST_TEST_FILES "/test_m3u/subdir.m3u");
    ASSERT_EQ(1u, discs.size());
    EXPECT_NE(std::string::npos, discs[0].find("disc3.gdi"));
}

TEST_F(M3uTest, RefusesEntryEscapingPlaylistDir)
{
    // ../../etc/passwd escapes the playlist's parent dir -> excluded
    EXPECT_TRUE(parseM3u(FLYCAST_TEST_FILES "/test_m3u/escape.m3u").empty());
}

TEST_F(M3uTest, RefusesNestedPlaylist)
{
    // an .m3u entry inside an .m3u -> excluded
    EXPECT_TRUE(parseM3u(FLYCAST_TEST_FILES "/test_m3u/nested.m3u").empty());
}

TEST_F(M3uTest, EmptyOrCommentsOnlyReturnsEmpty)
{
    EXPECT_TRUE(parseM3u(FLYCAST_TEST_FILES "/test_m3u/empty.m3u").empty());
}

TEST_F(M3uTest, MissingFileReturnsEmpty)
{
    EXPECT_TRUE(parseM3u(FLYCAST_TEST_FILES "/test_m3u/does_not_exist.m3u").empty());
}
```

- [ ] **Step 3: Register the test and the new sources in CMake**

Edit `tests/CMakeLists.txt` — add after line 24 (`src/imgread/CueTest.cpp`):
```
        src/imgread/M3uTest.cpp
```
Edit `core/imgread/CMakeLists.txt` — the list currently closes its paren on the last entry (`isofs.h)` at line 12), so move the `)` off `isofs.h` and add the two files (alphabetical, after `isofs.h`). Result:
```cmake
        isofs.cpp
        isofs.h
        playlist.cpp
        playlist.h)
```

- [ ] **Step 4: Create the header `core/imgread/playlist.h`**

```cpp
#pragma once
#include <string>
#include <vector>
#include <optional>

// Parse an .m3u playlist into an ordered list of resolved, bootable disc paths.
// Entries are resolved relative to the playlist's own directory (like cue.cpp
// resolves its track files). Blank lines, '#' comments and a UTF-8 BOM are
// skipped; surrounding quotes are stripped. Entries that escape the playlist
// directory, are themselves an .m3u, or lack a bootable disc extension
// (.chd/.gdi/.cdi/.cue/.elf) are refused. Returns empty on an unreadable,
// empty, or comments-only playlist.
std::vector<std::string> parseM3u(const std::string& path);

// A disc reference persisted alongside a savestate (see playlist sidecar).
struct DiscRef {
    int index = 0;
    std::string path;
};

// Sidecar (savestate disc persistence) — pure helpers, no file I/O here.
std::string encodeDiscSidecar(int index, const std::string& path);
std::optional<DiscRef> decodeDiscSidecar(const std::string& contents);

// Decide whether to remount on load: returns the disc index to insert, or
// nullopt to leave the mounted disc as-is. Remounts only when the saved index
// is in range, the saved path still matches discPaths[index] (guards against a
// reordered/changed playlist, mirroring libretro.cpp:2317), and it differs from
// the currently mounted index.
std::optional<int> resolveRemount(const DiscRef& saved,
                                  const std::vector<std::string>& discPaths,
                                  int currentIndex);
```

- [ ] **Step 5: Run the tests to verify they FAIL to compile/link**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cmake -B /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -G Ninja -DENABLE_CTEST=ON -DCMAKE_BUILD_TYPE=Release -DUSE_HOST_SDL=ON -DUSE_VULKAN=OFF -DUSE_LUA=OFF -S /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast &&
cmake --build /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -j "$(nproc)"'
```
Expected: link error — `parseM3u` etc. undefined (declared, not implemented). That is the RED state.

- [ ] **Step 6: Implement `core/imgread/playlist.cpp` (parser only for now)**

Implement `parseM3u` with `std::ifstream`, skipping BOM/comments/blanks, stripping quotes and CR, resolving each entry relative to `hostfs::storage().getParentPath(path)` via `getSubPath` (mirror `cue.cpp:78,181`), and refusing: entries that resolve outside the playlist dir, entries whose extension is `m3u`, and entries whose extension is not in `{chd,gdi,cdi,cue,elf}` or that do not exist. Use `get_file_extension` (`core/stdclass.h:125`). Leave the sidecar functions as minimal stubs for now (implemented in Task 4) so the file links — or implement them here if convenient; Task 4 adds their tests either way.

- [ ] **Step 7: Build and run ONLY the new tests — verify GREEN**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cmake --build /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -j "$(nproc)" &&
ctest --test-dir /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -R M3uTest --output-on-failure'
```
Expected: all `M3uTest.*` parser cases PASS. Path resolution uses the **real** desktop `StdStorage`/`AllStorage` (`core/oslib/storage.cpp:167,192,371,379`) compiled into the flycast target the tests link into — there is no storage stub in `test_stubs.cpp`. This is the same infrastructure `CueTest`/`OpenDisc` exercise, so resolving entries exactly as `cue.cpp:78,181` does (`getParentPath`/`getSubPath`) means the same real storage covers `parseM3u`.

- [ ] **Step 8: Commit**

```bash
cd /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast
git add core/imgread/playlist.h core/imgread/playlist.cpp core/imgread/CMakeLists.txt \
        tests/src/imgread/M3uTest.cpp tests/CMakeLists.txt tests/files/test_m3u
git commit -m "Add .m3u playlist parser for the standalone"
```

---

## Task 2: Playlist state + boot on `Emulator`

Hold the playlist on `Emulator`, expose a small public API, and make `loadGame` boot disc 0 of a `.m3u`. Boot needs a machine, so this is verified by build + the CT 1103 integration pass (Task 6), not a unit test; the pure index logic it relies on is covered in Tasks 1 and 4.

**Files:** Modify `core/emulator.h`, `core/emulator.cpp`.

- [ ] **Step 1: Add state + public API to `Emulator` (`core/emulator.h`)**

In the `public:` section (near `insertGdrom`, ~line 183) add:
```cpp
    // Multi-disc playlist (populated when a .m3u is loaded).
    const std::vector<std::string>& discList() const { return discPaths; }
    int currentDisc() const { return discIndex; }
    // Swap to playlist disc i: hot-swaps the media and records the index.
    void selectDisc(int i);
    // Mark the mounted disc as "not one of the playlist discs" (e.g. after the
    // whole-library picker inserts an arbitrary ROM).
    void invalidateDiscIndex() { discIndex = -1; }
```
In the `private:` section (near the other members, ~line 217) add:
```cpp
    std::vector<std::string> discPaths;
    int discIndex = 0;
```
`#include "imgread/playlist.h"` is not needed in the header (only `<vector>`/`<string>`, already included).

- [ ] **Step 2: Implement `selectDisc` and populate/clear the playlist (`core/emulator.cpp`)**

Add near `insertGdrom` (~:1106):
```cpp
void Emulator::selectDisc(int i)
{
    if (i < 0 || i >= (int)discPaths.size())
        return;
    insertGdrom(discPaths[i]);
    discIndex = i;
}
```
In `loadGame`, right where the incoming path is first handled (`core/emulator.cpp:568-570`), detect `.m3u` before `settings.content.path` is used, include `"imgread/playlist.h"` at the top of the file, and rewrite the effective path to disc 0:
```cpp
    discPaths.clear();
    discIndex = 0;
    if (path != nullptr && strlen(path) > 0 && get_file_extension(path) == "m3u")
    {
        discPaths = parseM3u(path);
        if (discPaths.empty())
            throw FlycastException(i18n::Ts("This media cannot be loaded"));
        path = discPaths[0].c_str();   // boot disc 0 through the normal path
    }
```
(Place this so `path` is reassigned before `settings.content.path = path` at :570. Everything downstream — `getFileInfo`, `getGamePlatform`, `initDrive` — then operates on disc 0.)

In `unloadGame` (`core/emulator.cpp:743`) clear the playlist so a later single-disc launch cannot inherit a stale list:
```cpp
    discPaths.clear();
    discIndex = 0;
```

- [ ] **Step 3: Build — verify it compiles and the existing suite stays green**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cmake --build /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -j "$(nproc)" &&
ctest --test-dir /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build --output-on-failure'
```
Expected: builds; all existing tests + M3uTest still pass. (Boot itself is exercised in Task 6.)

- [ ] **Step 4: Commit**

```bash
git add core/emulator.h core/emulator.cpp
git commit -m "Boot .m3u playlists to the first disc in the standalone"
```

---

## Task 3: Disc-swap submenu in the in-game menu (`gui.cpp`)

Add a submenu listing this game's discs beside the existing Insert/Eject control, and keep the active-disc marker honest when the whole-library picker is used. ImGui UI — verified by build + Task 6, no unit test.

**Files:** Modify `core/ui/gui.cpp`.

- [ ] **Step 1: Add the playlist submenu near the Insert/Eject control (`gui.cpp:620`)**

Immediately after the Insert/Eject `IconButton` block (ends ~:631), add:
```cpp
        // Multi-disc playlist swap (only when a .m3u with >1 disc is loaded)
        if (emu.discList().size() > 1)
        {
            for (int i = 0; i < (int)emu.discList().size(); i++)
            {
                std::string label = std::string(T("Disc")) + " " + std::to_string(i + 1);
                if (i == emu.currentDisc())
                    label += "  *";   // active-disc marker
                if (IconButton(ICON_FA_COMPACT_DISC, label, ScaledVec2(buttonWidth, 50)).realize())
                {
                    try {
                        emu.selectDisc(i);
                        gui_setState(GuiState::Closed);
                    } catch (const FlycastException& e) {
                        gui_error(e.what());
                    }
                }
            }
        }
```
(Match the surrounding `IconButton`/`ScaledVec2`/`T()` idiom; adjust the exact call to whatever the neighbouring buttons use.)

- [ ] **Step 2: Invalidate the disc index on a whole-library swap (`gui.cpp:1001`)**

Where the SelectDisk picker inserts an arbitrary ROM (`emu.insertGdrom(game.path);` at :1001), add right after it:
```cpp
                                    emu.invalidateDiscIndex();
```
So a manual whole-library swap sets `discIndex = -1`, the marker disappears, and no misleading sidecar is written.

- [ ] **Step 3: Build — verify it compiles and tests stay green**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cmake --build /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -j "$(nproc)" &&
ctest --test-dir /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build --output-on-failure'
```
Expected: builds; tests unchanged/green.

- [ ] **Step 4: Commit**

```bash
git add core/ui/gui.cpp
git commit -m "Add in-game disc-swap submenu for .m3u playlists"
```

---

## Task 4: Savestate sidecar helpers (pure, strict TDD)

Encode/decode the `<state>.disc` sidecar and decide the remount. Pure logic, fully unit-tested — no filesystem, no booted machine.

**Files:** Modify `core/imgread/playlist.cpp` (implement the stubs from Task 1), `tests/src/imgread/M3uTest.cpp` (add cases).

- [ ] **Step 1: Add the failing tests to `tests/src/imgread/M3uTest.cpp`**

```cpp
TEST_F(M3uTest, SidecarRoundTrip)
{
    std::string enc = encodeDiscSidecar(1, "/roms/shenmue/disc2.chd");
    auto ref = decodeDiscSidecar(enc);
    ASSERT_TRUE(ref.has_value());
    EXPECT_EQ(1, ref->index);
    EXPECT_EQ("/roms/shenmue/disc2.chd", ref->path);
}

TEST_F(M3uTest, DecodeRejectsEmptyOrMalformed)
{
    EXPECT_FALSE(decodeDiscSidecar("").has_value());
    EXPECT_FALSE(decodeDiscSidecar("notanumber\n/x").has_value());
}

TEST_F(M3uTest, ResolveRemountReturnsIndexWhenPathMatchesAndDiffers)
{
    std::vector<std::string> discs { "/r/d1.chd", "/r/d2.chd" };
    auto r = resolveRemount(DiscRef{1, "/r/d2.chd"}, discs, /*currentIndex=*/0);
    ASSERT_TRUE(r.has_value());
    EXPECT_EQ(1, *r);
}

TEST_F(M3uTest, ResolveRemountSkipsWhenAlreadyMounted)
{
    std::vector<std::string> discs { "/r/d1.chd", "/r/d2.chd" };
    EXPECT_FALSE(resolveRemount(DiscRef{1, "/r/d2.chd"}, discs, /*currentIndex=*/1).has_value());
}

TEST_F(M3uTest, ResolveRemountSkipsWhenPathNoLongerMatches)
{
    std::vector<std::string> discs { "/r/d1.chd", "/r/OTHER.chd" };  // playlist changed
    EXPECT_FALSE(resolveRemount(DiscRef{1, "/r/d2.chd"}, discs, 0).has_value());
}

TEST_F(M3uTest, ResolveRemountSkipsWhenIndexOutOfRange)
{
    std::vector<std::string> discs { "/r/d1.chd" };
    EXPECT_FALSE(resolveRemount(DiscRef{5, "/r/d2.chd"}, discs, 0).has_value());
}
```

- [ ] **Step 2: Build + run — verify the new cases FAIL (stubs return wrong/empty)**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cmake --build /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -j "$(nproc)" &&
ctest --test-dir /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -R M3uTest --output-on-failure'
```
Expected: the 6 new cases FAIL, the parser cases still pass. RED.

- [ ] **Step 3: Implement the sidecar helpers in `core/imgread/playlist.cpp`**

Two-line text format (index on line 1, path on line 2 — path may contain spaces, so it is the remainder):
```cpp
std::string encodeDiscSidecar(int index, const std::string& path)
{
    return std::to_string(index) + "\n" + path + "\n";
}

std::optional<DiscRef> decodeDiscSidecar(const std::string& contents)
{
    size_t nl = contents.find('\n');
    if (nl == std::string::npos)
        return std::nullopt;
    try {
        size_t consumed = 0;
        int index = std::stoi(contents.substr(0, nl), &consumed);
        if (consumed == 0)
            return std::nullopt;
        std::string path = contents.substr(nl + 1);
        while (!path.empty() && (path.back() == '\n' || path.back() == '\r'))
            path.pop_back();
        if (path.empty())
            return std::nullopt;
        return DiscRef{index, path};
    } catch (...) {
        return std::nullopt;
    }
}

std::optional<int> resolveRemount(const DiscRef& saved,
                                  const std::vector<std::string>& discPaths,
                                  int currentIndex)
{
    if (saved.index < 0 || saved.index >= (int)discPaths.size())
        return std::nullopt;
    if (discPaths[saved.index] != saved.path)
        return std::nullopt;              // playlist changed/reordered
    if (saved.index == currentIndex)
        return std::nullopt;              // already mounted
    return saved.index;
}
```

- [ ] **Step 4: Build + run — verify GREEN**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cmake --build /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -j "$(nproc)" &&
ctest --test-dir /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -R M3uTest --output-on-failure'
```
Expected: all `M3uTest.*` (parser + sidecar) PASS.

- [ ] **Step 5: Commit**

```bash
git add core/imgread/playlist.cpp tests/src/imgread/M3uTest.cpp
git commit -m "Add savestate disc-sidecar encode/decode/remount helpers"
```

---

## Task 5: Wire the sidecar into `dc_savestate` / `dc_loadstate` (`nullDC.cpp`)

Write the sidecar on save and remount on load, both guarded to regular numbered slots (`index >= 0`). Integration-verified (Task 6); the decision logic is already unit-tested (Task 4).

**Files:** Modify `core/nullDC.cpp`.

- [ ] **Step 1: Write the sidecar in `dc_savestate` (`core/nullDC.cpp:181`)**

Add `#include "imgread/playlist.h"` at the top. After `filename = hostfs::getSavestatePath(index, true);` is known for a regular slot, once the state file write has succeeded (after `zipFile.Close()`/`free(data)`, before the final `return;` ~`nullDC.cpp:251`), write the sidecar when the guard holds.

**IMPORTANT — `dc_savestate` is a `goto fail` function** (`goto fail;` at `nullDC.cpp:232,234,241,243` jump forward to the `fail:` label). C++ forbids a `goto` from crossing the initialization of a variable with a non-trivial initializer, so the sidecar's `std::string`/`hostfs::File*` locals **must** live inside their own `{ }` scope that opens *after* the last `goto fail;` and closes before `return;`. Without the braces the file will not compile (`error: jump to label 'fail' crosses initialization of 'std::string sidecar'`).

```cpp
    // Persist the active playlist disc alongside numbered-slot savestates.
    // Own scope: keeps these non-trivial locals out of the goto-fail range above.
    if (index >= 0 && emu.currentDisc() >= 0 && emu.discList().size() > 1)
    {
        std::string sidecar = filename + ".disc";
        std::string body = encodeDiscSidecar(emu.currentDisc(),
                                             emu.discList()[emu.currentDisc()]);
        hostfs::File *sf = hostfs::storage().openFile(sidecar.c_str(), "wb");
        if (sf != nullptr) {
            sf->write(body.data(), 1, body.size());
            delete sf;
        }
    }
```
(`index == -2` uses the in-RAM path where `filename == "RAM"`, and the `index >= 0` guard already excludes it.)

- [ ] **Step 2: Remount in `dc_loadstate` (`core/nullDC.cpp:264`, after `emu.loadstate(deser)` at :359)**

After the `try { … emu.loadstate(deser); … }` restores the machine, for a regular slot read the sidecar and remount via the pure decision helper:
```cpp
    if (index >= 0)
    {
        std::string sidecar = hostfs::getSavestatePath(index, false) + ".disc";
        hostfs::File *sf = hostfs::storage().openFile(sidecar.c_str(), "rb");
        if (sf != nullptr) {
            std::string body;
            char buf[512];
            size_t n;
            while ((n = sf->read(buf, 1, sizeof(buf))) > 0)
                body.append(buf, n);
            delete sf;
            auto ref = decodeDiscSidecar(body);
            if (ref) {
                auto target = resolveRemount(*ref, emu.discList(), emu.currentDisc());
                if (target)
                    emu.selectDisc(*target);
            }
        }
    }
```
The `index >= 0` guard matters because `emu.loadstate` at :359 also runs for `index == -1` (the GGPO net-sync load); the guard keeps the remount to numbered slots. `dc_savestate(-1)` is never called anywhere in `core/`, so no sidecar ever exists for a negative slot regardless (spec §8).

- [ ] **Step 2b: Guard the RAM (`index == -2`) path**

Confirm the new save/load blocks sit outside the `index == -2` branches (`nullDC.cpp:205,273`) or are otherwise skipped by the `index >= 0` guard, so the in-RAM quicksave path is untouched.

- [ ] **Step 3: Build — verify it compiles and the whole suite stays green**

```bash
nix-shell -p cmake ninja pkg-config gcc SDL2 curl zlib alsa-lib libevdev udev libao --run '
cmake --build /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build -j "$(nproc)" &&
ctest --test-dir /home/aldo/Dev/aldoborrero/flycast-romm/.claude/code/flycast/build --output-on-failure'
```
Expected: builds; all tests green.

- [ ] **Step 4: Commit**

```bash
git add core/nullDC.cpp
git commit -m "Persist and restore the active playlist disc across savestates"
```

---

## Task 6: Integration verification on the CT 1103 (real hardware)

Unit tests cover the pure logic; boot, live swap, and savestate remount need a real multi-disc game. Verify on the Selkies smoke LXC (CT 1103, `192.168.120.216`, ssh via `~/.ssh/claude`) with a real `.m3u` (e.g. Shenmue). This is a manual smoke, not an automated step.

- [ ] **Step 1: Build the standalone (not the test build) on/for the LXC and launch a real `.m3u`.** Confirm it boots to disc 1 (no "unknown disk format" rejection).
- [ ] **Step 2:** Open the in-game menu; confirm the "Disc 1 / Disc 2 …" submenu appears and that selecting another disc hot-swaps it (the running game detects the media change).
- [ ] **Step 3:** Save a state while on disc 2; reload it; confirm disc 2 is mounted (the `<state>.disc` sidecar exists next to the `.state`, and the game reads disc 2 correctly).
- [ ] **Step 4:** Regression — launch a single-disc game after the `.m3u`; confirm no stale playlist (submenu absent, normal boot).
- [ ] **Step 5:** Record the result (a note in the PR description; optionally a short clip for the flycast-romm gallery).

---

## Finish

After Task 6 passes, use @superpowers:finishing-a-development-branch to complete the branch: verify the full `ctest` suite is green, then push `feat/m3u-multidisc` to the `aldoborrero/flycast` fork and open the PR against `flyinghead/flycast`. The PR description should: link #423, summarize the 5 components, state that the parser + sidecar logic is unit-tested and boot/swap/remount are integration-verified on real hardware, call out the deliberate parser strictness vs. `read_m3u` and the two documented out-of-scope savestate paths (quicksave-RAM, GGPO net-sync), and disclose AI assistance per the user's standing decision.
