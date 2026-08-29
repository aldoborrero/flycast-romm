# Redesign — savestate disc-persistence as header metadata (supersedes the sidecar) + adversarial-review fixes

- **Date:** 2026-08-30
- **Supersedes:** the `<state>.disc` **sidecar** approach in `2026-08-28-flycast-m3u-multidisc-design.md` §3.5.
- **Target:** `flyinghead/flycast` fork `aldoborrero/flycast`, branch `feat/m3u-multidisc` (PR #1). Standalone only.
- **Trigger:** the CT 1103 hardware smoke verified #423 end-to-end (boot / submenu / swap / remount), but (a) an adversarial review found real bugs in the sidecar, and (b) a reference study of how mature emulators persist the disc showed the sidecar is not the best shape.

## 1. Why we're changing the savestate approach

The original spec chose a `<state>.disc` **sidecar file** (index+path) to avoid touching `libGDR_serialize` (shared with the libretro core) and the global savestate version `V59`. The hardware smoke worked, but two things pushed a rethink:

**Adversarial review found real defects in the sidecar** (all triangulated across three independent reviewers):
- **Crash + memory leak:** `dc_loadstate` called `emu.selectDisc()` outside the try/catch; `insertDisk` throws `FlycastException` on a missing/corrupt disc → propagates uncaught past `free(data)`.
- **Stale sidecar:** the `.disc` is written only under a guard but the `.state` is always written and the sidecar is never deleted → a resave without a playlist disc leaves a stale `.disc` that load still honors → remounts a disc over an unrelated state.
- **Unchecked write, orphaning:** the sidecar write ignores its return; a moved/copied `.state` loses or dangles its `.disc`.

**Reference study — how four emulators persist the mounted disc across savestates:**

| Emulator | Where the disc lives | What it stores | Notes |
|---|---|---|---|
| Flycast (current) | separate `.disc` sidecar, per slot | index + path (text) | the bugs above |
| RetroArch | separate `.ldci` sidecar, per game | index + path (JSON) | reconcile between sessions; the core does **not** serialize the disc |
| **DuckStation** | **header of the `.state` file** | `.m3u` path + subimage index | reconcile with graceful fallback; **outside** the core-state stream |
| PCSX2 | **nothing in the state** (identity in the filename `SERIAL(CRC)`) | serial+CRC in filename; version/screenshot as separate zip entries | no `.m3u`, no reconcile — the "don't" baseline for multi-disc |

**The consensus across the three mature emulators: never serialize the disc into the core-state stream** — it's a versioning trap (a variable-length string mid-stream forces version bumps and invalidates old states), paths go stale on library moves, and the disc is an external device orthogonal to CPU/RAM state. All three keep disc identity **outside** the versioned stream. This directly contradicts the "serialize it in the stream" suggestion an upstream-focused reviewer raised — the state of the art goes the other way.

DuckStation is the closest precedent (native `.m3u`, same problem) and shows the best shape: **metadata in the savestate file's header**, not a separate sidecar and not the core stream.

## 2. The design — disc info in the standalone `SavestateHeader`

Flycast's standalone savestate already has its own header, `SavestateHeader` (`core/nullDC.cpp`): `magic "FLYSAVE1"`, `creationDate`, `version`, `pngSize`, then png, then the `dc_serialize` stream. Crucially it is **standalone-only** — the libretro core uses `retro_serialize`/`dc_serialize` directly and never writes this header. So extending it touches neither `libGDR_serialize`, the global version enum, nor libretro.

**Header change (`FLYSAVE1` → `FLYSAVE2`):**
- Add, after `pngSize`: `s32 discIndex` (−1 = no playlist / not applicable) and `u32 discPathLen`. The disc path bytes (the resolved mounted disc, i.e. `discPaths[discIndex]`) follow the fixed header, before the png. Optionally also store the `.m3u` path for re-derivation fallback.
- `isValid()` accepts **both** `FLYSAVE1` and `FLYSAVE2`; a helper reports v2. Header I/O reads the common fixed prefix first, checks the magic, then reads the v2 tail only for `FLYSAVE2`. Legacy `FLYSAVE1` states load with no disc info (no reconcile), exactly as today.
- Write path (`dc_savestate`, numbered slots): if a playlist is active (`discList().size() > 1 && currentDisc() >= 0`) write `FLYSAVE2` with `discIndex` + the resolved disc path; otherwise write `FLYSAVE2` with `discIndex = -1` (single format going forward).

**Reconcile on load (`dc_loadstate`, after `emu.loadstate`):**
- If `discIndex >= 0` and the recorded disc path differs from what's mounted, remount it via `emu.selectDisc`/`insertGdrom` — **wrapped in try/catch** (fixes the crash+leak of the sidecar), with **graceful fallback** à la DuckStation: on failure, warn and keep the current disc; never hard-fail a load that otherwise deserialized. Update `discIndex` afterward so the submenu marker matches.

**Why this is strictly better than the sidecar:** self-contained (one file, no orphan/stale/moved-away `.disc`), reads cheaply from a fixed header offset, stays entirely standalone (no `libGDR_serialize`/V59/libretro impact), and lets the remount live inside the deserialize flow where exceptions are already handled.

## 3. Adversarial-review fixes to fold in (independent of the savestate change)

- **`parseM3u` absolute paths (🟠):** libretro's `read_m3u` handles `path_is_absolute`; ours doesn't → a `.m3u` with absolute entries boots in libretro but loads empty here. Handle absolute paths like libretro (or reject them explicitly and document the divergence). The `escapesPlaylistDir` guard currently only rejects `..` and is contained "by luck" via `getSubPath`'s concatenation.
- **`parseM3u` via `hostfs` (🟠):** it opens the `.m3u` with a raw `std::ifstream`, unlike `cue.cpp` which reads through `hostfs::storage()`. Breaks on Android SAF / virtual storage. Use `hostfs::storage().openFile`.
- **fileName-freeze-on-leave (🔴):** `invalidateDiscIndex()` sets `discIndex = -1` but leaves `discPaths` populated, so the `diskChange` fileName-freeze (guarded on `discPaths.empty()`) keeps the stale playlist name after a whole-library insert. Gate the freeze on `discIndex >= 0` instead, or clear `discPaths` on the whole-library insert.
- **Naming (design):** the stable-savestate-name behavior is still required (a `.state` saved on disc 2 must load after the playlist boots on disc 1). Keep the `diskChange` freeze (now gated on `discIndex >= 0`), or — cleaner, matching DuckStation/PCSX2 — key the savestate name on the game/`.m3u` identity rather than the mounted disc filename. Decide during implementation.
- **Parser strictness:** the extension allowlist (`chd/gdi/cdi/cue/elf`) and the `..` refusal are stricter than `read_m3u`. Either align, or make the divergence a documented, deliberate hardening.

## 4. Hygiene (before the PR)

- Squash the fixup commits (drop the "planning leftover" one — AI/planning residue a maintainer won't want).
- `core/imgread/playlist.h` uses 4-space indent; the tree is tabs.
- `tests/CMakeLists.txt`: move `M3uTest.cpp` after `GdiTest.cpp` (alphabetical).

## 5. What stays from the original design

Parser + boot + the disc-swap submenu are unchanged and verified in hardware. `discPaths`/`discIndex` remain on `Emulator` for the submenu. `resolveRemount`'s decision logic (index in range, path matches, differs from mounted) carries over — it just reads its inputs from the header instead of a sidecar file, and the encode/decode-to-text helpers are replaced by the header struct I/O.

## 6. Testing

- Keep the pure unit tests for `parseM3u` and `resolveRemount` (adjust for the absolute-path and hostfs changes).
- Add unit coverage for the header round-trip (write `FLYSAVE2` with disc info → read back index+path; a `FLYSAVE1` blob reads as no-disc) where it can be exercised without a booted machine.
- Re-run the CT 1103 hardware smoke (boot / swap / **save on disc 2 → reload → remount**, now via the header) and re-run the adversarial review after the redesign.
