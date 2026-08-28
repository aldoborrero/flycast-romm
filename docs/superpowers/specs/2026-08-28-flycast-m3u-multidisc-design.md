# Design — .m3u multi-disc + disc-swap in Flycast standalone (upstream #423)

- **Date:** 2026-08-28
- **Status:** Draft for review
- **Target repo:** `flyinghead/flycast` (fork: `aldoborrero/flycast`) — an **upstream** contribution, not part of flycast-romm. This spec is the session's working design; it does not ship in the PR.
- **Issue:** [flyinghead/flycast#423](https://github.com/flyinghead/flycast/issues/423) — "Support loading multi-disc games with m3u files" (open, 18 comments, 0 PRs for the standalone).

## 1. Context and goal

`.m3u` is the de-facto standard for multi-disc games (libretro/RetroArch use it for disc-swap playlists). Flycast's **libretro core** already supports `.m3u` with live disc-swap; the **standalone** does not — a `.m3u` handed to it is rejected as an unknown disk format. #423 asks for this in the standalone and is much-requested but unattended.

A viability analysis (verified against the source) found the standalone **already has the entire hot-swap engine** #423 needs. Libretro adds no engine magic — only an `.m3u` parser and an adapter to RetroArch's `retro_disk_control` interface. So porting #423 is: **parse the `.m3u` + hold the playlist in state + boot disc 1 + add an in-game submenu that calls the existing `Emulator::insertGdrom(path)`**, plus persisting the disc index in savestates.

**Goal:** the standalone boots a `.m3u` multi-disc game to its first disc and lets the player switch discs live from the in-game menu, exactly as the libretro core does — resolving #423 for the whole Flycast community, not just RomM.

## 2. What already exists (verified, do not rebuild)

- **`Emulator::insertGdrom(path)`** (`core/emulator.cpp:1106`) → `gdr::insertDisk(path)` + `diskChange()`: mounts another disc hot, signals the GD-ROM "media change requested" at the ATA level, which the running game detects. This is the whole hot-swap path, and the standalone UI already calls it.
- **`gdr::insertDisk` / `doDiscSwap` / `OpenDisc`** (`core/imgread/common.cpp:369,87`): open a chd/gdi/cdi/cue and mount it.
- **UI already has manual disc-swap** (`core/ui/gui.cpp:620-631`): an in-game "Eject/Insert Disk" control that, on insert, sends the user to `GuiState::SelectDisk` to pick from the *entire* ROM library and calls `emu.insertGdrom(game.path)` (`gui.cpp:1001`). #423 replaces that "pick from the whole library" step with a short list of *this game's* discs.
- **Disc-parsing tests exist**: `tests/src/imgread/CueTest.cpp`, `GdiTest.cpp`, with fixtures in `tests/files/` — the natural home for a new `M3uTest.cpp`.

## 3. Components (5 pieces, reusing the engine)

```
loadGame(path) ── if .m3u ──▶ parseM3u() ──▶ discPaths[], discIndex = 0
                                                  │
                                    content.path = discPaths[0]
                                                  │
                                    initDrive(discPaths[0])   (boot disc 1, unchanged)

in-game menu (gui.cpp) ──▶ "Disc 1/2/3…" submenu ──▶ emu.insertGdrom(discPaths[i]); discIndex = i
                                                            (existing hot-swap)

savestate: serialize/deserialize discIndex alongside the GD-ROM state
```

### 3.1 `parseM3u(path) -> std::vector<std::string>`

New helper in `core/imgread/` (beside `OpenDisc`). Ported from libretro's `read_m3u` (`shell/libretro/libretro.cpp:3866`) but using `hostfs`/`std::ifstream` instead of RetroArch's VFS. Reads line by line; skips blank lines, `#` comments, and a UTF-8 BOM; strips surrounding quotes; resolves each entry **relative to the playlist's own directory**; keeps only entries under the ROM tree with a bootable extension (`.chd/.gdi/.cdi/.cue/.elf`); refuses an entry that is itself a `.m3u`. Returns the ordered disc list (empty if unreadable/empty). This mirrors the containment/BOM/comment handling already proven in flycast-romm and the webstation broker.

### 3.2 Playlist state

`std::vector<std::string> discPaths` + `int discIndex` held on `Emulator` (or `settings.content`). Populated when `loadGame` sees a `.m3u`; `discIndex` starts at 0; cleared in `unloadGame` (`core/emulator.cpp:743`).

### 3.3 Boot

`discPaths[0]` goes through the normal path (`content.path = discPaths[0]` before `getGamePlatform`/`initDrive`). **Zero engine changes.**

### 3.4 Disc-swap UI

In `core/ui/gui.cpp`, beside the existing "Eject/Insert Disk" control (`gui.cpp:620`), show a submenu of the playlist's disc labels when `discPaths.size() > 1`. Selecting disc `i` calls `emu.insertGdrom(discPaths[i])` and sets `discIndex = i`. Reuses the existing pattern; no new engine call.

### 3.5 Savestate + discIndex

Persist `discIndex` in the savestate so loading a state remounts the disc that was in the drive when it was saved. Libretro does not do this robustly; doing it here avoids a state loading with the wrong disc mounted. Serialize alongside the existing GD-ROM state (`libGDR_serialize`, `core/imgread/common.cpp:396`), guarded for version compatibility so older states still load (absent field → keep disc 0 / current). This is the most delicate area and gets its own test.

## 4. Hook points (verified, file:line)

| Concern | Location | Change |
|---|---|---|
| Parse `.m3u`, populate playlist, boot disc 0 | `Emulator::loadGame` (`core/emulator.cpp:562`) | detect `.m3u`, call `parseM3u`, set `content.path = discPaths[0]` |
| Hot-swap | `Emulator::insertGdrom` (`emulator.cpp:1106`) | **reuse, 0 LOC** |
| Disc-swap submenu | `core/ui/gui.cpp:620` | add playlist submenu |
| Clear playlist | `unloadGame` (`emulator.cpp:743`) | reset `discPaths`/`discIndex` |
| Persist disc index | `libGDR_serialize` (`common.cpp:396`) | version-guarded field |

## 5. Testing

- **TDD, parser**: new `tests/src/imgread/M3uTest.cpp` (pattern of `CueTest.cpp`/`GdiTest.cpp`) with fixtures under `tests/files/`. Cases: first disc; comments/blanks/CRLF/BOM; relative and subdir entries; an entry escaping the ROM tree (refused); a nested playlist (refused); empty/unreadable playlist; disc ordering. Built with `-DENABLE_CTEST=ON`.
- **Integration** (boot + disc-swap UI + savestate index): verified end-to-end on the CT 1103 (real multi-disc `.m3u`, e.g. Shenmue): boots disc 1, the submenu swaps discs live, and a savestate taken on disc 2 reloads on disc 2.
- **F0 (build)**: the real infra risk — get the emulator to compile with `-DENABLE_CTEST=ON` (whole emulator + tests). This is step 0 of the plan; if it fights, resolve before implementing.

## 6. Approach notes / decisions

- **Parser hook in `loadGame`**, not `getGamePlatform` — `loadGame` is where content resolution belongs and where `content.path` is set.
- **Reuse `insertGdrom`**, do not add a new engine path — the hot-swap already works and is already UI-driven.
- **Standalone vs libretro is simpler, not harder**: no need to implement the full `retro_disk_control` interface (eject/index/add/replace/labels) — the standalone just needs a list, an index, and direct `insertGdrom` calls.
- **Savestate index included** (per decision), version-guarded for backward compatibility.

## 7. Risks

1. **Build (F0)**: compiling the whole emulator with `ENABLE_CTEST` is the main infra risk (large C++ build, no nix flake; deps resolved via nix by hand). De-risk first.
2. **Savestate compatibility**: the serialized `discIndex` must be version-guarded so pre-change states still load; the new field defaults to keeping the current disc.
3. **Path resolution / hostfs**: the standalone uses `hostfs::storage()` (content-URIs on Android), unlike libretro's flat `g_roms_dir`. Resolve entries via the same mechanism `loadGame` already uses.
4. **Playlist lifetime**: clear on `unloadGame`/reset so a later single-disc launch does not inherit a stale list.

## 8. Out of scope

- Non-Dreamcast platforms (NAOMI/Atomiswave are single-image; `.m3u` is a disc-based-console concern).
- Changing the existing "pick from whole library" disc picker (kept as-is; the playlist submenu is additive).
- The webstation-broker `.m3u` workaround and the RomM `_EMULATOR_CAPABILITIES` entry (separate contributions; this upstream fix would eventually make the webstation workaround unnecessary).

## 9. References

- Issue: flyinghead/flycast#423.
- libretro model: `shell/libretro/libretro.cpp` (`read_m3u` :3866, `init_disk_control_interface` :3835, `retro_set_image_index` :3728, `retro_set_eject_state` :3698).
- Engine: `core/emulator.cpp` (`loadGame` :562, `insertGdrom` :1106, `openGdrom` :1114, `unloadGame` :743); `core/imgread/common.cpp` (`OpenDisc` :87, `gdr::insertDisk`/`doDiscSwap` :369, `libGDR_serialize` :396); `core/hw/gdrom/gdromv3.cpp` (`gd_setdisc`/`gd_disc_change` :316,366).
- UI: `core/ui/gui.cpp` (Eject/Insert control :620, SelectDisk insert :1001).
- Tests: `tests/CMakeLists.txt`, `tests/src/imgread/{CueTest,GdiTest}.cpp`, `tests/files/`.
