# Design — .m3u multi-disc + disc-swap in Flycast standalone (upstream #423)

- **Date:** 2026-08-28
- **Status:** Draft for review
- **Target repo:** `flyinghead/flycast` (fork: `aldoborrero/flycast`) — an **upstream** contribution, not part of flycast-romm. This spec is the session's working design; it does not ship in the PR.
- **Issue:** [flyinghead/flycast#423](https://github.com/flyinghead/flycast/issues/423) — "Support loading multi-disc games with m3u files" (open, 18 comments, 0 PRs for the standalone).

## 1. Context and goal

`.m3u` is the de-facto standard for multi-disc games (libretro/RetroArch use it for disc-swap playlists). Flycast's **libretro core** already supports `.m3u` with live disc-swap; the **standalone** does not — a `.m3u` handed to it is rejected as an unknown disk format. #423 asks for this in the standalone and is much-requested but unattended.

A viability analysis (verified against the source) found the standalone **already has the entire hot-swap engine** #423 needs. Libretro adds no engine magic — only an `.m3u` parser and an adapter to RetroArch's `retro_disk_control` interface. So porting #423 is: **parse the `.m3u` + hold the playlist in state + boot disc 1 + add an in-game submenu that calls the existing `Emulator::insertGdrom(path)`**.

**Goal:** the standalone boots a `.m3u` multi-disc game to its first disc and lets the player switch discs live from the in-game menu, exactly as the libretro core does — resolving #423 for the whole Flycast community, not just RomM.

**Scope decision:** persisting the active disc index in savestates is **deferred to a follow-up PR** (§8, §9). It is not a serialized-field one-liner — it needs active remount-on-load logic and a decision about where the index lives relative to the libretro-shared serializer — and it is orthogonal to the core value of #423 (boot + live swap). This PR ships the boot + swap; the follow-up adds savestate persistence.

## 2. What already exists (verified, do not rebuild)

- **`Emulator::insertGdrom(path)`** (`core/emulator.cpp:1106`) → `gdr::insertDisk(path)` + `diskChange()`: mounts another disc hot, signals the GD-ROM "media change requested" at the ATA level, which the running game detects. This is the whole hot-swap path, and the standalone UI already calls it.
- **`gdr::insertDisk` / `doDiscSwap` / `OpenDisc`** (`core/imgread/common.cpp:369,156,87`): open a chd/gdi/cdi/cue and mount it.
- **UI already has manual disc-swap** (`core/ui/gui.cpp:620-631`): an in-game "Eject/Insert Disk" control that, on insert, sends the user to `GuiState::SelectDisk` to pick from the *entire* ROM library; the real `emu.insertGdrom(...)` call lives under that state at `gui.cpp:998-1006` (`:1001`). #423 adds a *shorter* path — a submenu of *this game's* discs that calls `insertGdrom` directly, modeled on that same call — leaving the whole-library picker untouched.
- **Disc-parsing tests exist**: `tests/src/imgread/CueTest.cpp`, `GdiTest.cpp`, with fixtures in `tests/files/` — the natural home for a new `M3uTest.cpp`. `CueTest` calls `OpenDisc` directly; `M3uTest` follows that pattern.

## 3. Components (4 pieces, reusing the engine)

```
loadGame(path) ── if .m3u ──▶ parseM3u() ──▶ discPaths[], discIndex = 0
                                                  │
                                    content.path = discPaths[0]
                                                  │
                                    initDrive(discPaths[0])   (boot disc 1, unchanged)

in-game menu (gui.cpp) ──▶ "Disc 1/2/3…" submenu ──▶ emu.insertGdrom(discPaths[i]); discIndex = i
                                                            (existing hot-swap, modeled on gui.cpp:1001)
```

### 3.1 `parseM3u(path) -> std::vector<std::string>`

New helper in `core/imgread/` (beside `OpenDisc`). Ported from libretro's `read_m3u` (`shell/libretro/libretro.cpp:3866`) but using `hostfs`/`std::ifstream` instead of RetroArch's VFS, and **stricter** than `read_m3u` (which is a permissive line reader): skip blank lines, `#` comments, and a UTF-8 BOM; strip surrounding quotes; resolve each entry **relative to the playlist's own directory** via `hostfs::storage().getParentPath`/`getSubPath` (`core/oslib/storage.h:132-133`); keep only entries under the ROM tree with a bootable extension (`.chd/.gdi/.cdi/.cue/.elf`); refuse an entry that is itself a `.m3u`. Returns the ordered disc list (empty if unreadable/empty). This mirrors the containment/BOM/comment handling already proven in flycast-romm and the webstation broker. The extra strictness vs. `read_m3u` is deliberate (containment safety) and is called out in the PR so a reviewer expecting a 1:1 port is not surprised.

### 3.2 Playlist state

`std::vector<std::string> discPaths` + `int discIndex` held on `Emulator`. Purely **runtime** state in this PR (no serialization). Populated when `loadGame` sees a `.m3u`; `discIndex` starts at 0; both cleared in `unloadGame` (`core/emulator.cpp:743`) so a later single-disc launch cannot inherit a stale list.

### 3.3 Boot

`discPaths[0]` goes through the normal path (`content.path = discPaths[0]` before `getGamePlatform`/`initDrive`). **Zero engine changes.**

### 3.4 Disc-swap UI

In `core/ui/gui.cpp`, beside the existing "Eject/Insert Disk" control (`gui.cpp:620`), show a submenu of the playlist's disc labels when `discPaths.size() > 1`. Selecting disc `i` calls `emu.insertGdrom(discPaths[i])` (the same call used at `gui.cpp:1001`) and sets `discIndex = i`. Reuses the existing pattern; no new engine call.

**Interaction with the existing whole-library picker:** the current "Insert Disk → SelectDisk → pick any ROM → `insertGdrom(game.path)`" path (`gui.cpp:1001`) does **not** know about `discPaths`, so a swap made through it leaves `discIndex` pointing at the wrong entry. Rather than track an out-of-band disc through the playlist, the submenu shows the active-disc marker (e.g. a check on `discIndex`) as a hint only, and a manual whole-library swap sets `discIndex = -1` ("not one of the playlist discs") so the marker simply disappears instead of lying. No functional breakage either way — this is purely the correctness of the "which disc is in the drive" indicator.

## 4. Hook points (verified, file:line)

| Concern | Location | Change |
|---|---|---|
| Parse `.m3u`, populate playlist, boot disc 0 | `Emulator::loadGame` (`core/emulator.cpp:562`) | detect `.m3u`, call `parseM3u`, set `content.path = discPaths[0]` |
| Hot-swap engine | `Emulator::insertGdrom` (`emulator.cpp:1106`) | **reuse, 0 LOC** |
| Disc-swap submenu | `core/ui/gui.cpp:620` (control site); call modeled on `gui.cpp:998-1006`/`:1001` | add playlist submenu that calls `emu.insertGdrom(discPaths[i])` directly |
| Manual-picker desync | `core/ui/gui.cpp:1001` | set `discIndex = -1` when a whole-library swap runs, so the active-disc marker stays honest |
| Clear playlist | `unloadGame` (`emulator.cpp:743`) | reset `discPaths`/`discIndex` |

## 5. Testing

- **TDD, parser**: new `tests/src/imgread/M3uTest.cpp` (pattern of `CueTest.cpp`/`GdiTest.cpp`, which call `OpenDisc` directly) with fixtures under `tests/files/`. Cases: first disc; comments/blanks/CRLF/BOM; surrounding quotes; relative and subdir entries; an entry escaping the ROM tree (refused); a nested `.m3u` (refused); empty/unreadable playlist; disc ordering. Built with `-DENABLE_CTEST=ON`.
- **Integration** (boot + disc-swap UI): verified end-to-end on the CT 1103 (real multi-disc `.m3u`, e.g. Shenmue): boots disc 1, the submenu swaps discs live, and the running game detects the media change.
- **F0 (build)**: the real infra risk — get the emulator to compile with `-DENABLE_CTEST=ON`. Note `tests/CMakeLists.txt:10` adds the test sources **into the whole flycast target**, so F0 builds the entire emulator + tests, not a small test binary. This is step 0 of the plan; if it fights, resolve before implementing.

## 6. Approach notes / decisions

- **Parser hook in `loadGame`**, not `getGamePlatform` — `loadGame` is where content resolution belongs and where `content.path` is set.
- **Reuse `insertGdrom`**, do not add a new engine path — the hot-swap already works and is already UI-driven; our submenu calls the exact same function the manual picker calls.
- **Standalone vs libretro is simpler, not harder**: no need to implement the full `retro_disk_control` interface (eject/index/add/replace/labels) — the standalone just needs a list, an index, and direct `insertGdrom` calls.
- **Savestate index deferred** (per decision): keeps this PR to boot + swap, which is self-contained, low-risk, and the bulk of the #423 value. See §8/§9.

## 7. Risks

1. **Build (F0)**: compiling the whole emulator with `ENABLE_CTEST` is the main infra risk (large C++ build, no nix flake; deps resolved via nix by hand). De-risk first.
2. **Path resolution / hostfs**: the standalone uses `hostfs::storage()` (content-URIs on Android), unlike libretro's flat `g_roms_dir`. Resolve entries via `hostfs::storage().getParentPath`/`getSubPath` (`core/oslib/storage.h:132-133`), the same mechanism `loadGame` already uses — do not hand-roll path joins.
3. **Active-disc indicator vs. manual picker**: the existing whole-library picker (`gui.cpp:1001`) bypasses `discPaths`; handled by setting `discIndex = -1` on a manual swap (§3.4) so the indicator never lies. Cosmetic only — no functional path depends on `discIndex` in this PR.
4. **Playlist lifetime**: clear on `unloadGame`/reset so a later single-disc launch does not inherit a stale list.

## 8. Out of scope

- **Savestate disc-index persistence — deferred to a follow-up PR.** It is not a serialized-field addition: `libGDR_serialize` (`core/imgread/common.cpp:396`) serializes only the null-drive disc type / q-subchannel / scheduler, **not** the content path, and loadstate remounts nothing — so persisting the disc requires *active remount-on-load* logic (repopulate `discPaths` and call `insertGdrom(discPaths[discIndex])` in deserialize when it differs), plus a decision on where `discIndex` lives, since `libGDR_serialize` is **shared with libretro** and has no `Emulator` access and its version bump is global. See §9 for the intended follow-up approach.
- Non-Dreamcast platforms (NAOMI/Atomiswave are single-image; `.m3u` is a disc-based-console concern).
- Changing the existing "pick from whole library" disc picker (kept as-is; the playlist submenu is additive).
- The webstation-broker `.m3u` workaround and the RomM `_EMULATOR_CAPABILITIES` entry (separate contributions; this upstream fix would eventually make the webstation workaround unnecessary).

## 9. Follow-up: savestate disc-index (next PR)

Recorded here so the follow-up starts with the analysis already done:

- **Where the index lives:** put `discIndex` (and enough to repopulate `discPaths`) somewhere reachable from `common.cpp` without an `Emulator` handle — e.g. a field on the global `settings.content`, serialized under a version guard. Do **not** widen `libGDR_serialize`'s responsibility across the libretro boundary without confirming the libretro core round-trips the same field, since the savestate version enum (`serialize.h`, `Current=V59`) is global to both frontends.
- **Active remount on load:** deserialize must, if the persisted disc differs from what is mounted, call the hot-swap (`insertGdrom(discPaths[discIndex])`) rather than assume the drive already holds the right disc — a state can be loaded cold, with `discPaths` empty, so the load path must be able to repopulate the playlist first.
- **Thread-safety:** the deferred/scheduled nature of the swap (`sh4_sched_request`, `common.cpp:379`) means a swap kicked off from deserialize must be sequenced safely relative to the rest of state restore; this is the extra risk that justified splitting it out.
- **Backward compat:** older states (absent field) must load and keep disc 0 / the currently mounted disc.

## 10. References

- Issue: flyinghead/flycast#423.
- libretro model: `shell/libretro/libretro.cpp` (`read_m3u` :3866, `init_disk_control_interface` :3835, `retro_set_image_index` :3728, `retro_set_eject_state` :3698).
- Engine: `core/emulator.cpp` (`loadGame` :562, `insertGdrom` :1106, `openGdrom` :1114, `unloadGame` :743); `core/imgread/common.cpp` (`OpenDisc` :87, `doDiscSwap` :156, `gdr::insertDisk` :369, `libGDR_serialize` :396); `core/oslib/storage.h` (`getParentPath`/`getSubPath` :132-133).
- UI: `core/ui/gui.cpp` (Eject/Insert control :620, SelectDisk insert :998-1006/:1001).
- Tests: `tests/CMakeLists.txt` (:10 sources into the flycast target), `tests/src/imgread/{CueTest,GdiTest}.cpp`, `tests/files/`.
