# Design — .m3u multi-disc + disc-swap in Flycast standalone (upstream #423)

- **Date:** 2026-08-28
- **Status:** Draft for review
- **Target repo:** `flyinghead/flycast` (fork: `aldoborrero/flycast`) — an **upstream** contribution, not part of flycast-romm. This spec is the session's working design; it does not ship in the PR.
- **Issue:** [flyinghead/flycast#423](https://github.com/flyinghead/flycast/issues/423) — "Support loading multi-disc games with m3u files" (open, 18 comments, 0 PRs for the standalone).

## 1. Context and goal

`.m3u` is the de-facto standard for multi-disc games (libretro/RetroArch use it for disc-swap playlists). Flycast's **libretro core** already supports `.m3u` with live disc-swap; the **standalone** does not — a `.m3u` handed to it is rejected as an unknown disk format. #423 asks for this in the standalone and is much-requested but unattended.

A viability analysis (verified against the source) found the standalone **already has the entire hot-swap engine** #423 needs. Libretro adds no engine magic — only an `.m3u` parser and an adapter to RetroArch's `retro_disk_control` interface. So porting #423 is: **parse the `.m3u` + hold the playlist in state + boot disc 1 + add an in-game submenu that calls the existing `Emulator::insertGdrom(path)` + remount the active disc when a savestate is loaded**.

**Goal:** the standalone boots a `.m3u` multi-disc game to its first disc, lets the player switch discs live from the in-game menu, and reloads a savestate onto the disc that was in the drive when it was saved — exactly as the libretro core + RetroArch pair does, resolving #423 for the whole Flycast community, not just RomM.

## 2. What already exists (verified, do not rebuild)

- **`Emulator::insertGdrom(path)`** (`core/emulator.cpp:1106`) → `gdr::insertDisk(path)` + `diskChange()`: mounts another disc hot, signals the GD-ROM "media change requested" at the ATA level, which the running game detects. This is the whole hot-swap path, and the standalone UI already calls it.
- **`gdr::insertDisk` / `doDiscSwap` / `OpenDisc`** (`core/imgread/common.cpp:369,156,87`): open a chd/gdi/cdi/cue and mount it.
- **UI already has manual disc-swap** (`core/ui/gui.cpp:620-631`): an in-game "Insert/Eject Disk" control that, on insert, sends the user to `GuiState::SelectDisk` to pick from the *entire* ROM library; the real `emu.insertGdrom(...)` call lives under that state at `gui.cpp:998-1006` (`:1001`). #423 adds a *shorter* path — a submenu of *this game's* discs that calls `insertGdrom` directly — leaving the whole-library picker untouched.
- **Standalone savestates are frontend code, separate from libretro.** `dc_savestate`/`dc_loadstate` (`core/nullDC.cpp:181,264`) are the standalone's own save/load entry points; they call `hostfs::getSavestatePath(index, writable)` (`core/oslib/oslib.cpp:198`, decl `oslib.h:58`) for the `.state` path, then `emu.loadstate(deser)` (`nullDC.cpp:359`) to restore. The global `Emulator emu` (`core/emulator.h:223`) is already used throughout `nullDC.cpp`. This is what makes the sidecar approach (§3.5) cheap and keeps it entirely out of the libretro-shared `libGDR_serialize`.
- **Disc-parsing tests exist**: `tests/src/imgread/CueTest.cpp`, `GdiTest.cpp` (both call `OpenDisc` directly), with fixtures in `tests/files/` — the natural home for a new `M3uTest.cpp`.

## 3. Components (5 pieces, reusing the engine)

```
loadGame(path) ── if .m3u ──▶ parseM3u() ──▶ discPaths[], discIndex = 0
                                                  │
                                    content.path = discPaths[0]
                                                  │
                                    initDrive(discPaths[0])   (boot disc 1, unchanged)

in-game menu (gui.cpp) ──▶ "Disc 1/2/3…" submenu ──▶ emu.insertGdrom(discPaths[i]); discIndex = i
                                                            (existing hot-swap, modeled on gui.cpp:1001)

dc_savestate(index) ──▶ write <state>.disc sidecar { discIndex, discPaths[discIndex] }
dc_loadstate(index) ──▶ after emu.loadstate: read sidecar, verify path, insertGdrom if it differs
```

### 3.1 `parseM3u(path) -> std::vector<std::string>`

New helper in `core/imgread/` (beside `OpenDisc`). Ported from libretro's `read_m3u` (`shell/libretro/libretro.cpp:3866`) but using `hostfs`/`std::ifstream` instead of RetroArch's VFS, and **stricter** than `read_m3u` (a permissive line reader): skip blank lines, `#` comments, and a UTF-8 BOM; strip surrounding quotes; resolve each entry **relative to the playlist's own directory** via `hostfs::storage().getParentPath`/`getSubPath` (`core/oslib/storage.h:132-133`); keep only entries under the ROM tree with a bootable extension (`.chd/.gdi/.cdi/.cue/.elf`); refuse an entry that is itself a `.m3u`. Returns the ordered disc list (empty if unreadable/empty). Mirrors the containment/BOM/comment handling already proven in flycast-romm and the webstation broker. The extra strictness vs. `read_m3u` is deliberate (containment safety) and is called out in the PR so a reviewer expecting a 1:1 port is not surprised.

### 3.2 Playlist state

`std::vector<std::string> discPaths` + `int discIndex` held on `Emulator` (`core/emulator.h`), reachable via the global `emu` from `gui.cpp` (UI), `emulator.cpp` (boot) and `nullDC.cpp` (savestate). Populated when `loadGame` sees a `.m3u`; `discIndex` starts at 0; both cleared in `unloadGame` (`core/emulator.cpp:743`) so a later single-disc launch cannot inherit a stale list. Not part of the serialized machine state — persistence rides in the sidecar (§3.5), not in the savestate binary.

### 3.3 Boot

`discPaths[0]` goes through the normal path (`content.path = discPaths[0]` before `getGamePlatform`/`initDrive`). **Zero engine changes.**

### 3.4 Disc-swap UI

In `core/ui/gui.cpp`, beside the existing "Insert/Eject Disk" control (`gui.cpp:620`), show a submenu of the playlist's disc labels when `discPaths.size() > 1`. Selecting disc `i` calls `emu.insertGdrom(discPaths[i])` (the same call used at `gui.cpp:1001`) and sets `discIndex = i`. Reuses the existing pattern; no new engine call.

**Interaction with the existing whole-library picker:** the current "Insert Disk → SelectDisk → pick any ROM → `insertGdrom(game.path)`" path (`gui.cpp:1001`) does not know about `discPaths`, so a swap made through it would leave `discIndex` stale. Setting `discIndex = -1` there ("not one of the playlist discs") keeps the active-disc marker honest (it simply disappears) and makes the sidecar skip persistence for an out-of-playlist disc rather than record a wrong index.

### 3.5 Savestate disc persistence — sidecar (RetroArch pattern)

Persist the active disc **outside** the savestate binary, mirroring how RetroArch does it via `retro_set_initial_image` (`libretro.cpp:2311-2320,3790`): store **both index and path**, and on load only remount if the stored path still matches the playlist entry.

- **Save** (`dc_savestate`, `nullDC.cpp:181`): guarded to regular numbered slots (`index >= 0`) — the reserved negative slots have no on-disk `.state` to sidecar (§8). After `filename = hostfs::getSavestatePath(index, true)`, if a playlist is active (`discPaths` non-empty and `discIndex >= 0`), write a sidecar `<filename>.disc` containing `discIndex` and `discPaths[discIndex]`. No sidecar for single-disc games or an out-of-playlist manual disc (`discIndex == -1`).
- **Load** (`dc_loadstate`, `nullDC.cpp:264`): also guarded by `index >= 0`. After `emu.loadstate(deser)` (`:359`) restores the machine, read `<filename>.disc`; if present and its stored path equals `discPaths[storedIndex]` (guard against a reordered/changed playlist, exactly as libretro's `disk_paths[...].compare(disk_initial_path)` at `libretro.cpp:2317`), and that disc differs from what is mounted, call `emu.insertGdrom(discPaths[storedIndex])` and set `discIndex = storedIndex`. The `index >= 0` guard matters because `emu.loadstate` at `:359` also runs for `index == -1`; without it the code path would be reached (though no sidecar exists there anyway — see §8).
- **Why after `emu.loadstate`, not inside serialize:** the remount runs in the standalone frontend flow, on the same thread and via the same `insertGdrom` a manual UI swap uses — so it inherits the existing swap's timing/scheduling behaviour and needs no reasoning about `sh4_sched` ordering *during* deserialize.
- **Backward compatible for free:** an older savestate has no sidecar → nothing is remounted → today's behaviour. Touches neither `libGDR_serialize` nor the global savestate version (`core/serialize.h`, `Current=V59`), so libretro is unaffected.

## 4. Hook points (verified, file:line)

| Concern | Location | Change |
|---|---|---|
| Parse `.m3u`, populate playlist, boot disc 0 | `Emulator::loadGame` (`core/emulator.cpp:562`) | detect `.m3u`, call `parseM3u`, set `content.path = discPaths[0]` |
| Playlist state | `Emulator` (`core/emulator.h`) | add `discPaths`/`discIndex`, reachable via global `emu` |
| Hot-swap engine | `Emulator::insertGdrom` (`emulator.cpp:1106`) | **reuse, 0 LOC** |
| Disc-swap submenu | `core/ui/gui.cpp:620` (control site); call modeled on `gui.cpp:998-1006`/`:1001` | add playlist submenu that calls `emu.insertGdrom(discPaths[i])` directly |
| Manual-picker desync | `core/ui/gui.cpp:1001` | set `discIndex = -1` on a whole-library swap |
| Write disc sidecar | `dc_savestate` (`core/nullDC.cpp:181`) | guard `index >= 0`; after `getSavestatePath`, write `<state>.disc` when a playlist is active |
| Remount on load | `dc_loadstate` (`core/nullDC.cpp:264`, after `emu.loadstate` at `:359`) | guard `index >= 0`; read `<state>.disc`, verify path, `insertGdrom` if it differs |
| Clear playlist | `unloadGame` (`emulator.cpp:743`) | reset `discPaths`/`discIndex` |

## 5. Testing

- **TDD, parser**: new `tests/src/imgread/M3uTest.cpp` (pattern of `CueTest.cpp`/`GdiTest.cpp`, which call `OpenDisc` directly) with fixtures under `tests/files/`. Cases: first disc; comments/blanks/CRLF/BOM; surrounding quotes; relative and subdir entries; an entry escaping the ROM tree (refused); a nested `.m3u` (refused); empty/unreadable playlist; disc ordering. Built with `-DENABLE_CTEST=ON`.
- **TDD, sidecar**: unit-test the sidecar read/write helper directly — round-trip `{index, path}`; absent sidecar (legacy state) → no-op; stored path not matching `discPaths[storedIndex]` → skip remount; `discIndex == -1` → no sidecar written. The remount decision (differs-from-mounted → `insertGdrom`) is the logic under test; keep it in a small pure helper so it does not require a booted machine.
- **Integration** (boot + disc-swap UI + savestate remount): verified end-to-end on the CT 1103 (real multi-disc `.m3u`, e.g. Shenmue): boots disc 1, the submenu swaps discs live, and a savestate taken on disc 2 reloads with disc 2 mounted.
- **F0 (build)**: the real infra risk — get the emulator to compile with `-DENABLE_CTEST=ON`. Note `tests/CMakeLists.txt:10` adds the test sources **into the whole flycast target**, so F0 builds the entire emulator + tests. This is step 0 of the plan; if it fights, resolve before implementing.

## 6. Approach notes / decisions

- **Parser hook in `loadGame`**, not `getGamePlatform` — `loadGame` is where content resolution belongs and where `content.path` is set.
- **Reuse `insertGdrom`**, do not add a new engine path — the hot-swap already works and is already UI-driven; both the submenu and the load-time remount call the exact same function the manual picker calls.
- **Savestate via sidecar (option B), not by widening `libGDR_serialize` (option A).** Option A would put the field in the libretro-shared serializer (no `Emulator` access) and bump the global savestate version `V59`, affecting the libretro core. The sidecar keeps the whole feature in the standalone frontend (`nullDC.cpp`), is backward compatible for free, and copies RetroArch's own index+path+verify pattern. This is why persisting the disc is affordable here (~1 helper + two short call sites) rather than an engine change.
- **Standalone vs libretro is simpler, not harder**: no need to implement the full `retro_disk_control` interface (eject/index/add/replace/labels) — the standalone just needs a list, an index, direct `insertGdrom` calls, and a sidecar.

## 7. Risks

1. **Build (F0)**: compiling the whole emulator with `ENABLE_CTEST` is the main infra risk (large C++ build, no nix flake; deps resolved via nix by hand). De-risk first.
2. **Path resolution / hostfs**: the standalone uses `hostfs::storage()` (content-URIs on Android), unlike libretro's flat `g_roms_dir`. Resolve entries via `hostfs::storage().getParentPath`/`getSubPath` (`core/oslib/storage.h:132-133`) — do not hand-roll path joins. The sidecar path derives from `getSavestatePath`, so it follows the same storage abstraction.
3. **Active-disc indicator / sidecar vs. manual picker**: the whole-library picker (`gui.cpp:1001`) bypasses `discPaths`; handled by `discIndex = -1` (§3.4), which also suppresses a misleading sidecar.
4. **Sidecar/playlist drift**: a savestate whose sidecar path no longer matches the playlist entry (game moved, `.m3u` reordered) must not mount the wrong disc — the index+path match guard (§3.5, mirroring `libretro.cpp:2317`) falls back to leaving the mounted disc as-is.
5. **Playlist lifetime**: clear on `unloadGame`/reset so a later single-disc launch does not inherit a stale list.

## 8. Out of scope

- **The reserved negative savestate slots (`index < 0`) carry no `.disc` sidecar**, which is why the write and read are guarded by `index >= 0` (§3.5). The only two negative slots are:
  - `index == -2` — the `HAS_FMEMOPEN` **in-RAM quicksave** buffer (`nullDC.cpp:205,273`): no on-disk `.state` file to sidecar. The playlist still lives in `emu` for the session, so an in-RAM quickload rarely needs a remount; covering it (e.g. a RAM-side index snapshot) is a follow-up, documented as a known gap.
  - `index == -1` — the **GGPO initial net-sync load at game start** (`core/emulator.cpp:681`, `dc_loadstate(-1)` under `config::GGPOEnable`, reading `<name>.state.net`), whose peer-sync MD5 is computed at `nullDC.cpp:308-314`. Note this is *not* rollback: GGPO rollback save/load bypasses `dc_savestate`/`dc_loadstate` entirely and calls `dc_serialize`/`dc_deserialize` directly (`core/network/ggpo.cpp:315,349`), so the sidecar can never touch rollback. And `dc_savestate(-1)` is never called anywhere in `core/`, so no sidecar is ever written for a negative slot; disc-swap persistence across netplay is a separate concern, out of scope.
- Non-Dreamcast platforms (NAOMI/Atomiswave are single-image; `.m3u` is a disc-based-console concern).
- Changing the existing "pick from whole library" disc picker (kept as-is; the playlist submenu is additive).
- The webstation-broker `.m3u` workaround and the RomM `_EMULATOR_CAPABILITIES` entry (separate contributions; this upstream fix would eventually make the webstation workaround unnecessary).

## 9. References

- Issue: flyinghead/flycast#423.
- libretro model: `shell/libretro/libretro.cpp` (`read_m3u` :3866, `init_disk_control_interface` :3835, `retro_set_image_index` :3728, `retro_set_eject_state` :3698, `retro_set_initial_image` :3790, initial-image restore with path-match guard :2311-2320).
- Engine: `core/emulator.cpp` (`loadGame` :562, `insertGdrom` :1106, `openGdrom` :1114, `unloadGame` :743, global `emu` :1144); `core/emulator.h` (`extern Emulator emu` :223); `core/imgread/common.cpp` (`OpenDisc` :87, `doDiscSwap` :156, `gdr::insertDisk` :369, `libGDR_serialize` :396); `core/oslib/storage.h` (`getParentPath`/`getSubPath` :132-133).
- Savestate (standalone): `core/nullDC.cpp` (`dc_savestate` :181, `dc_loadstate` :264, `index == -2` RAM slot :205/:273, GGPO net-sync MD5 :308-314, `emu.loadstate` :359); `core/oslib/oslib.cpp` (`getSavestatePath` :198); `core/emulator.cpp` (GGPO `dc_loadstate(-1)` at boot :681); `core/network/ggpo.cpp` (rollback `dc_deserialize`/`dc_serialize` :315,349, bypassing the standalone save/load path); `core/serialize.h` (`Current=V59` :79).
- UI: `core/ui/gui.cpp` (Insert/Eject control :620, SelectDisk insert :998-1006/:1001).
- Tests: `tests/CMakeLists.txt` (:10 sources into the flycast target), `tests/src/imgread/{CueTest,GdiTest}.cpp`, `tests/files/`.
