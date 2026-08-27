# Design — Packaging Selkies natively in Nix

- **Date:** 2026-08-27
- **Status:** Draft for review (rev 2, after spec review)
- **Sub-project:** #1 of the native deployment effort. This spec covers **only** packaging the Selkies streaming stack as Nix derivations. The system-manager module, the broker adaptation, session/service wiring (systemd, GPU driver env) and Flycast configuration are sub-project #2 and are out of scope here.

## 1. Context and goal

`flycast-romm` (and its sibling `pcsx2-romm-integration`) ships today as a **LinuxServer Docker mod**: a Go broker plus an s6 service tree injected into `lscr.io/linuxserver/flycast`. That base image supplies the streaming stack — **Selkies** — for free. We want to run the whole thing **natively on a Debian LXC managed by `system-manager`** (declarative, no Docker), which means we must reproduce Selkies ourselves.

Selkies is not in nixpkgs, and it was rewritten: the current version drops GStreamer/WebRTC-by-default and is built from three coupled Python/Rust pieces, a web UI, and a small gamepad interposer. **Packaging those is the blocking prerequisite for the native deployment and is valuable on its own** (reusable, potentially upstreamable). This spec scopes that packaging work.

**Goal:** produce Nix derivations for the Selkies streaming stack, exposed as flake packages, that run headless on an AMD APU (amdgpu, VAAPI) and can host a single fullscreen Wayland app (Flycast) using **pixelflux's built-in Wayland compositor**, with H.264 VAAPI encode and a browser-embeddable stream.

**Non-goals (sub-project #2):** the `system-manager` module and its systemd units; the broker's env adaptation; GPU driver env on Debian (`LIBVA_DRIVERS_PATH`, render-node perms); Flycast's own configuration; TLS/reverse-proxy for RomM's iframe. Note: the *compositor itself* is **in scope** because it ships inside pixelflux; only the *session/service wiring around it* is deferred.

## 2. Streaming decision (settled upstream of this spec)

Prior analysis fixed the streaming choice: **Selkies is the most performant option that stays embeddable in RomM's iframe.** Render is Flycast on the GPU (OpenGL, or Vulkan with a pinned resolution to avoid flycast bug #648); the display is **pixelflux's own headless Wayland (Smithay) compositor**, which Flycast joins as a client; capture is pixelflux's zero-copy dmabuf; encode is **H.264 VAAPI** (not AV1/HEVC — no LAN benefit, AMD AV1 fragile); transport is WebSocket first, `--mode=webrtc` as the latency-optimal target. This spec only packages the software that makes that possible.

## 3. Version decision — target the LinuxServer-validated set

The three Python pieces evolve coupled, and their published versions do **not** all line up. Data provenance: the **`main` column is verified from the local clone `a779435`**; the **`348bc4f` column is verified by `git fetch`-ing that exact commit and reading its `pyproject.toml`, cross-checked against `docker-baseimage-selkies` `Dockerfile` + `package_versions.txt`.** (The local `.claude/code/selkies` working tree is `main`, not `348bc4f` — the earlier draft misattributed this.)

| Piece | `main` (a779435) | **LSIO-shipped (348bc4f)** — chosen |
|---|---|---|
| selkies deps | `pixelflux~=2.1.0`, `pcmflux~=2.1.0` | `pixelflux`, `pcmflux` **unconstrained** → resolve to **2.0.0** |
| webrtc/ice/xlib | aiortc/aioice/python-xlib **all vendored** | `webrtc` (aiortc) **vendored**, but **`aioice>=0.10.1,<1.0.0` external**; python-xlib external |
| PyAV (`av`) | dropped | **present** (`"av"`, satisfied by system/nixpkgs `av`) |
| crypto floors | `cryptography>=50`, `pyopenssl>=26` | **`cryptography>=44.0.0`, `pyopenssl>=25.0.0`** (both met by nixpkgs) |
| pixelflux/pcmflux | need 2.1.x (**unpublished**) | **2.0.0** (`9d2caed` / `ee3d8d3`), manylinux wheels on PyPI |

**Decision:** target **selkies `348bc4f` + pixelflux/pcmflux `2.0.0`** — the only fully-consistent, production-shipped combination, *and* the lighter Python target (its `cryptography>=44`/`pyopenssl>=25` floors are met by nixpkgs today, whereas `main`'s `>=50`/`>=26` are not — see risk 4). Revisit a bump to `main` only if F3's WebRTC mode requires it.

Commits pinned:
- pixelflux `9d2caedcfe37ffa35f800625e05d0a61ba23af77` (v2.0.0)
- pcmflux `ee3d8d3c0e628f7e311b3efc54235c17d018aa5d` (v2.0.0)
- selkies `348bc4f61da66198573e7e57db9a266aca1991d5` (LSIO pin; `a779435` = `main`, reference)
- baseimage-selkies (reference) `69f4fc9cf895760d0ac105064cf40d70c54f432d`

## 4. Components

Six derivations, ordered by risk (de-risk the hardest first).

### 4.1 pixelflux (Rust/PyO3) — critical path

- **Build system:** `setuptools-rust` (NOT maturin). In Nix: `buildPythonPackage` (setuptools) + `rustPlatform.cargoSetupHook` + `setuptools-rust`. **pixelflux has a committed `Cargo.lock`**, so `cargoLock.lockFile` can point straight at it.
- **Encode:** H.264 VAAPI **via FFmpeg's `h264_vaapi`** (`ffmpeg-sys-next = 8.1`, features `avcodec,avfilter`), not direct libva. SW H.264 via `x264-sys` + `openh264-sys2`; JPEG via `turbojpeg`. NVENC via subcrate `nvcodec-sys`, **dlopen at runtime** (committed bindings, no libclang there).
- **Top friction — `smithay` git dependency** at rev `ca932e042fa9ad150605c150a86275b85f9ad5b3`. The Nix sandbox blocks Cargo's git fetch; vendor it via `cargoLock` + `outputHashes` for that git source (the committed `Cargo.lock` already records the rev).
- **libclang/LLVM at build:** `ffmpeg-sys-next` and `x264-sys` run bindgen → set `LIBCLANG_PATH`.
- **FFmpeg ceiling: ≤ 8.1** (`ffmpeg-sys-next 8.1` probes up to 8.1; the VAAPI path uses ~5.1-era API). nixpkgs default `ffmpeg` is **8.1.2** today (a patch of 8.1 → satisfies the major.minor probe). The real risk is a *future* nixpkgs bump to 8.2/9.0; pin `ffmpeg` explicitly.
- **Native build inputs:** `rustc`+`cargo`, `python3`, `cmake`, `nasm`, `pkg-config`, `llvmPackages.libclang`, `ffmpeg` (≤8.1), `x264`, `libdrm`, `mesa`/gbm, `wayland`, `wayland-protocols`, `libinput`, `libxkbcommon`, `libva`. `turbojpeg-sys`/`openh264-sys2` cmake/nasm-build vendored C by default; optionally wire system libs to skip.
- **Runtime (dlopen, not build deps):** `libva.so.2` + Mesa radeonsi VA driver, `libEGL`, `libgbm`, `libwayland`, optionally `libnvidia-encode.so.1`/`libcuda.so`. Driver paths on Debian are a sub-project #2 concern; the derivation must not hard-depend on NVIDIA.
- **License:** crate is MPL-2.0; bundled `nvcodec-sys/headers/nvEncodeAPI.h` keeps NVIDIA's proprietary license; x264 would pull GPL if `--enable-gpl` were set — LSIO deliberately avoids it, and so must we. Carry all three in `meta`.

### 4.2 pcmflux (Rust/PyO3) — simple

- Same `setuptools-rust` pattern. Pure Rust, no `build.rs`. Deps: `libpulse-binding`, `opus`/`audiopus_sys`.
- **No `Cargo.lock` committed** → we must generate and pin one (`cargoHash`/vendored lock) for reproducibility. (Contrast with pixelflux, which ships its lock.)
- **PyO3 version skew to note:** pcmflux pins `pyo3 = 0.27`, pixelflux `pyo3 = 0.29`. Both must build against a compatible `python3` ABI; not a blocker, but keep them on one interpreter.
- Native inputs: `rustc`+`cargo`, `python3`, `cmake`, `pkg-config`, `libpulseaudio`, `libopus` (provide it, else `audiopus_sys` cmake-builds a vendored copy).

### 4.3 selkies wheel (Python) — easy once 4.1–4.2 exist

- Pure-Python `buildPythonPackage`. At `348bc4f`: `webrtc` (aiortc) is **vendored**, but **`aioice`, `av` and python-xlib are external** deps.
- **Exact `dependencies` at `348bc4f`** (verified from `git show 348bc4f:pyproject.toml` — an *older* pyproject than `main`): `websockets>=13`, `gputil`, `prometheus_client`, `msgpack`, `pynput`, `psutil`, `watchdog`, `Pillow`, **`python-xlib @ <selkies fork URL>`** (see 4.6), `pixelflux`, `pcmflux` (both unconstrained → 2.0.0), `xkbcommon`, `distro`, `pulsectl`, `pasimple`, `aioice>=0.10.1,<1.0.0`, `av`, `cffi`, `cryptography>=44` (nixpkgs 49 ✓), `google-crc32c`, `pyee>=13`, `pylibsrtp>=0.10` (1.0 ✓), `pyopenssl>=25` (26.3 ✓), `aiohttp>=3.7`, `aiofiles>=25.1`. **Not present at `348bc4f`** (these are `main`-only, and an earlier draft wrongly listed them): `uvloop`, `pulsectl-asyncio`, `dnspython`, `ifaddr`, `nvidia-ml-py`.
- Console scripts: `selkies`, `selkies-resize` (no `selkies-gpu-probe` at this commit).
- **The web UI is NOT bundled in the wheel at `348bc4f`** (that's `main`'s `selkies_web` model). The server serves files from a `web_root` directory (`settings.py:130`, default `/opt/selkies-web`), so the wheel and the web bundle (4.4) are independent; wiring `--web_root` to the web package is sub-project #2's job.

### 4.4 web UI (npm/Vite → a served directory, not a wheel bundle)

- Three Vite packages under `addons/`: `selkies-web-core` (shared core), `selkies-dashboard` (the shipped one), `selkies-dashboard-wish` (also built by LSIO). **npm lockfiles are gitignored** upstream → generate and pin per package (`buildNpmPackage` + `npmDepsHash`).
- Build model at `348bc4f` (from LSIO's `Dockerfile`): `cd addons/selkies-web-core && npm install && npm run build`, then for each dashboard copy `../selkies-web-core/dist/selkies-core.js` into `src/`, `npm install && npm run build`, and collect `dist/` into an output dir. **The result is a static directory served via `web_root`**, not injected into the Python package. Our derivation produces `$out` = that static dir (the `selkies-dashboard` build), which #2 points `--web_root` at.

### 4.6 python-xlib fork (Python) — the emulator-input dep

- `348bc4f` declares `python-xlib @ https://github.com/selkies-project/python-xlib/archive/master.zip` — a **selkies fork**, not PyPI's `xlib`. `buildPythonPackage` cannot resolve a URL dep hermetically. Package the fork as its own derivation (`fetchFromGitHub selkies-project/python-xlib`) and inject it, and `postPatch` the wheel's `pyproject.toml` to drop the URL form (the dep is satisfied from `propagatedBuildInputs`). It provides the `Xlib` module the input path uses.

### 4.5 joystick interposer (C, `LD_PRELOAD`) — the emulator-critical bit

- LSIO builds `selkies_joystick_interposer.so` from `addons/js-interposer` and sets `SELKIES_INTERPOSER` (`Dockerfile:312,493`). It shims `/dev/input/js*` so the browser gamepad reaches the app. **For a gamepad-driven emulator this is functionally part of the stack**, so it belongs in sub-project #1: a small `stdenv.mkDerivation` producing the `.so`. Its *loading* (the `LD_PRELOAD` env on Flycast) is wired in #2.

## 5. Approach

- **Primary — build from source.** `buildPythonPackage`+`setuptools-rust` for pixelflux (using its committed lock) and pcmflux (generated lock, vendoring the `smithay` git dep for pixelflux), `buildPythonPackage` for the wheel, `buildNpmPackage` for the web UI, `mkDerivation` for the interposer.
- **Escape hatch — prebuilt manylinux wheels.** LSIO does not compile pixelflux/pcmflux; it installs the **PyPI manylinux wheels 2.0.0**. So `autoPatchelfHook` over those wheels is a legitimate, low-effort way to stand F1 up fast while the from-source path is finished. Documented, not the end state.

## 6. Repo structure

```
nix/packages/selkies/
├── pixelflux/      # buildPythonPackage + setuptools-rust, committed cargo lock (vendor smithay git)
├── pcmflux/        # buildPythonPackage + setuptools-rust, generated cargo lock
├── web/            # buildNpmPackage → static web_root directory
├── js-interposer/  # mkDerivation → selkies_joystick_interposer.so
├── python-xlib/    # buildPythonPackage of the selkies fork
└── default.nix     # the selkies wheel, taking pixelflux + pcmflux + python-xlib (+ web via web_root in #2)
```

Blueprint auto-discovers `nix/packages/`; each is a flake package (reusable/upstreamable), kept separate from sub-project #2's module.

## 7. Phased delivery

- **F1 — stream up, fastest path.** pixelflux/pcmflux via the manylinux-wheel escape hatch; selkies wheel + web UI + interposer from source; run a fullscreen Wayland app under **pixelflux's built-in compositor** (Wayland mode, activated by `PIXELFLUX_WAYLAND=true` — verified real at `348bc4f` in `selkies.py`; the native equivalent of the base image's Wayland path) and stream it over **WebSocket** with **software** encode (`SELKIES_USE_CPU`). Proves the whole chain end-to-end. Note: we omit LSIO's optional `labwc` WM (a fullscreen app needs no window manager) — a deliberate, minor deviation from LSIO's default path, validated in the F1 smoke.
- **F2 — from-source + VAAPI.** Replace the wheels with the from-source Rust derivations (vendor `smithay`, pin FFmpeg ≤8.1); enable **H.264 VAAPI** encode; confirm zero-copy dmabuf on amdgpu.
- **F3 — WebRTC (deferred, may need `main`).** Evaluate `--mode=webrtc`; this pulls in the **signaling plane** (`signaling_server.py`) and optional STUN/TURN config, and — if it requires `main`'s vendored aiortc/aioice and 2.1.x — a version bump planned as its own step.

## 8. Verification

- **pixelflux/pcmflux:** `pythonImportsCheck = [ "pixelflux" "pcmflux" ]`; hermetic build (no network).
- **wheel:** `selkies --help` runs; `import selkies` and `import Xlib` succeed (the fork).
- **web:** the web package's `$out/index.html` exists (served via `--web_root` in #2).
- **interposer:** the `.so` builds and exports the `js*` shim symbols.
- **VAAPI (F2):** on rhea's AMD APU, `vainfo` shows `VAProfileH264*` enc entrypoints (already confirmed); a smoke run encodes a test surface via `h264_vaapi` without JPEG fallback.
- **End-to-end (F1):** a throwaway Wayland client (e.g. a GL demo) rendered under **pixelflux's built-in compositor** is visible in a browser tab on the Selkies port, with a gamepad event passing through the interposer. Full Flycast wiring is sub-project #2.

## 9. Risks and open questions

1. **`smithay` git-dep vendoring** — the top pixelflux risk; the committed `Cargo.lock` records the rev, but `outputHashes` must be supplied. Wheel escape hatch keeps F1 unblocked if it fights.
2. **pcmflux has no committed lock** — must generate; a future upstream dep bump shifts it.
3. **FFmpeg ceiling ≤8.1** — nixpkgs is 8.1.2 (ok); a future bump to 8.2/9.0 breaks the crate probe. Pin explicitly.
4. **Python floors** — met for `348bc4f` (`cryptography>=44` vs nixpkgs 49; `pyopenssl>=25` vs 26.3; `pylibsrtp>=0.10` vs 1.0). **A bump to `main` would *not* be met** (`cryptography>=50`, `pyopenssl>=26`) → a `main` bump needs a `cryptography` override or nixpkgs advancing first. This is a concrete reason to hold at `348bc4f`.
5. **npm builds with gitignored lockfiles** — generate and pin; Vite 8 + React 19 trees are large.
6. **`main` vs `348bc4f` for WebRTC** — F3 may force a version bump *and* pull in the signaling/STUN/TURN plane; scoped as its own step.
7. **Licensing** — MPL-2.0 (pixelflux/pcmflux), NVIDIA proprietary header in `nvcodec-sys`, and GPL avoidance (no `--enable-gpl` on x264). Record in `meta`; must not accidentally enable x264 GPL.
8. **Closure size** — ffmpeg + mesa + wayland + the Rust toolchain make a large build closure; acceptable but worth measuring, especially for the LXC.
9. **PyO3 skew** (pixelflux 0.29 vs pcmflux 0.27) — build both against one `python3`.

## 10. References (paths relative to each repo root)

- **pixelflux** `9d2caed` — `pyproject.toml`, `pixelflux/Cargo.toml`, `Cargo.lock`, `src/encoders/vaapi.rs`, `src/encoders/nvenc.rs`, `nvcodec-sys/`
- **pcmflux** `ee3d8d3` — `pcmflux/Cargo.toml`, `pyproject.toml`, `src/lib.rs`
- **selkies** `348bc4f` (target, fetched) / `a779435` (`main`, local clone) — `pyproject.toml`, `src/selkies/stream_server.py`, `src/selkies/settings.py`, `scripts/ci/build-web.sh`, `addons/{selkies-web-core,selkies-dashboard,js-interposer}`
- **docker-baseimage-selkies** `69f4fc9` — `Dockerfile` (pins, seds, `--system-site-packages`, interposer, `startwm_wayland.sh`), `package_versions.txt`
