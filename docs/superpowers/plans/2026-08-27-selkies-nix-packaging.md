# Selkies Nix Packaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Package the rewritten Selkies streaming stack (pixelflux, pcmflux, the `selkies` wheel, the web UI, and the joystick interposer) as Nix flake packages that run headless on an AMD APU with H.264 VAAPI encode and a browser-embeddable stream.

**Architecture:** Six flake packages under `nix/packages/` (pixelflux, pcmflux, the `selkies` wheel, the web `web_root`, the joystick interposer, and the python-xlib fork), auto-discovered by blueprint. Delivered in two phases: **F1** stands the whole chain up fast using pixelflux/pcmflux **prebuilt manylinux wheels** (the escape hatch LSIO itself uses) plus the wheel/web/interposer from source; **F2** replaces the two wheels with **from-source Rust derivations** and turns on VAAPI. WebRTC (F3) is a separate future plan. Target the LinuxServer-validated set: selkies `348bc4f` + pixelflux/pcmflux `2.0.0`.

**Tech Stack:** Nix (blueprint flake, `buildPythonPackage`, `rustPlatform`/`setuptools-rust`, `buildNpmPackage`, `autoPatchelfHook`, `stdenv.mkDerivation`), Python 3, Rust, FFmpeg (≤8.1), Mesa/VAAPI, Wayland/Smithay.

**Spec:** `docs/superpowers/specs/2026-08-27-selkies-nix-packaging-design.md`

**Conventions for this plan:**
- "TDD" here = write the verification first (a `passthru.tests` check or a `nix build` invocation that must fail for the right reason), then the derivation, then build green, then commit.
- Hashes (`cargoHash`, `npmDepsHash`, wheel `sha256`, `outputHashes`) are discovered by building: start with `lib.fakeHash`, run the build, copy the real hash from the error, rebuild. Each such step says so.
- All `nix` commands run from the worktree root. Reference @superpowers:test-driven-development for the discipline and @superpowers:verification-before-completion before any "done" claim.

---

## File Structure

```
nix/packages/
├── docker-mod/                 # (existing — untouched)
├── selkies-js-interposer/
│   └── default.nix             # mkDerivation → selkies_joystick_interposer.so
├── selkies-web/
│   └── default.nix             # buildNpmPackage → static web_root directory (served in #2)
├── selkies-python-xlib/
│   └── default.nix             # buildPythonPackage of the selkies python-xlib fork
├── selkies-pcmflux/
│   └── default.nix             # buildPythonPackage + setuptools-rust (from source, F2)
├── selkies-pcmflux-wheel/
│   └── default.nix             # autoPatchelf manylinux wheel 2.0.0 (F1 escape hatch)
├── selkies-pixelflux/
│   └── default.nix             # buildPythonPackage + setuptools-rust (from source, F2)
├── selkies-pixelflux-wheel/
│   └── default.nix             # autoPatchelf manylinux wheel 2.0.0 (F1 escape hatch)
└── selkies/
    └── default.nix             # the selkies wheel, taking pixelflux/pcmflux/web/interposer as args
```

Each directory is a blueprint package. The `selkies` package takes its pixelflux/pcmflux inputs as overridable arguments so F1 (wheels) and F2 (from source) swap without editing the wheel derivation. Sources are pinned with `fetchFromGitHub`/`fetchPypi`; nothing fetches at eval time beyond fixed-output derivations.

---

## Task 0: Scaffold and confirm blueprint discovery

**Files:**
- Read: `flake.nix`, `nix/packages/docker-mod/default.nix` (learn the package signature blueprint passes: `{ pkgs, perSystem, flake, ... }`)
- Create: `nix/packages/selkies-js-interposer/default.nix` (stub)

- [ ] **Step 1: Read the existing package to learn the calling convention**

Read `nix/packages/docker-mod/default.nix` and `nix/package.nix` to confirm the argument set blueprint passes and how `perSystem.self.<name>` cross-references work.

- [ ] **Step 2: Write a stub package as the "failing test"**

Create `nix/packages/selkies-js-interposer/default.nix`:

```nix
{ pkgs, ... }:
pkgs.stdenv.mkDerivation {
  pname = "selkies-js-interposer";
  version = "0.0.0-stub";
  dontUnpack = true;
  installPhase = "mkdir -p $out";
  meta.description = "stub, to be implemented";
}
```

- [ ] **Step 3: Confirm blueprint exposes it**

Run: `nix flake show 2>&1 | grep selkies-js-interposer`
Expected: the package appears under `packages.x86_64-linux`.
Run: `nix build .#selkies-js-interposer -L`
Expected: builds (empty output).

- [ ] **Step 4: Commit**

```bash
git add nix/packages/selkies-js-interposer/default.nix
git commit -m "chore(nix): scaffold selkies-js-interposer package"
```

---

## Task 1: Joystick interposer (`selkies_joystick_interposer.so`)

The simplest real derivation — validates the package layout end to end. Source: `selkies` repo `addons/js-interposer` at `348bc4f`.

**Files:**
- Read: `.claude/code/selkies/addons/js-interposer/` (Makefile/sources; confirm the `.so` target and build command)
- Modify: `nix/packages/selkies-js-interposer/default.nix`

- [ ] **Step 1: Inspect the upstream build**

Run: `git -C .claude/code/selkies show 348bc4f61da66198573e7e57db9a266aca1991d5:addons/js-interposer/Makefile` (and list the dir) to get the exact `gcc`/`make` invocation and output filename.

- [ ] **Step 2: Write the real derivation**

```nix
{ pkgs, ... }:
pkgs.stdenv.mkDerivation (finalAttrs: {
  pname = "selkies-js-interposer";
  version = "0-unstable-348bc4f";   # no own version upstream
  src = pkgs.fetchFromGitHub {
    owner = "selkies-project"; repo = "selkies";
    rev = "348bc4f61da66198573e7e57db9a266aca1991d5";
    hash = pkgs.lib.fakeHash;   # fill from first build
  };
  sourceRoot = "${finalAttrs.src.name}/addons/js-interposer";
  # The upstream Makefile is: gcc -shared -fPIC -o selkies_joystick_interposer.so
  #   joystick_interposer.c -ldl  (confirm the exact target in Step 1)
  buildPhase = ''
    runHook preBuild
    make all
    runHook postBuild
  '';
  installPhase = ''
    runHook preInstall
    install -Dm0644 selkies_joystick_interposer.so \
      $out/lib/selkies_joystick_interposer.so
    runHook postInstall
  '';
  meta.description = "LD_PRELOAD shim mapping browser gamepads to /dev/input/js*";
})
```

- [ ] **Step 3: Build, fill the src hash, rebuild**

Run: `nix build .#selkies-js-interposer -L`
Expected first run: hash mismatch → copy the `got:` hash into `hash =`. Rebuild.
Expected: `$out/lib/selkies_joystick_interposer.so` exists (`ls result/lib`).

- [ ] **Step 4: Add a build-time check for the `.so`**

Add to the derivation:
```nix
  doInstallCheck = true;
  installCheckPhase = ''
    test -f $out/lib/selkies_joystick_interposer.so
    ${pkgs.file}/bin/file $out/lib/selkies_joystick_interposer.so | grep -q "shared object"
  '';
```
Run: `nix build .#selkies-js-interposer -L` → PASS.

- [ ] **Step 5: Commit**

```bash
git add nix/packages/selkies-js-interposer/default.nix
git commit -m "feat(nix): package the selkies joystick interposer"
```

---

## Task 2: pcmflux from source (validates the setuptools-rust/PyO3 pattern)

The smaller of the two Rust/PyO3 pieces, and the one whose pattern pixelflux reuses. Doing it first proves the toolchain (Rust + PyO3 + `setuptools-rust` under `buildPythonPackage`) with the fewest moving parts — **no smithay git-dep, no FFmpeg, no bindgen**. Source: `linuxserver/pcmflux` at `ee3d8d3` (v2.0.0). No committed `Cargo.lock` → generate one.

**Files:**
- Read: `.claude/code/pcmflux/{pyproject.toml,setup.py,pcmflux/Cargo.toml}`
- Create: `nix/packages/selkies-pcmflux/default.nix`
- Create: `nix/packages/selkies-pcmflux/Cargo.lock` (generated)

- [ ] **Step 1: Generate and vendor a Cargo.lock**

```bash
cp -r .claude/code/pcmflux /tmp/pcmflux-lock && cd /tmp/pcmflux-lock/pcmflux
cargo generate-lockfile
cp Cargo.lock <worktree>/nix/packages/selkies-pcmflux/Cargo.lock
```
(Uses the Rust from the devshell; the lock is committed for reproducibility.)

- [ ] **Step 2: Write the derivation as the failing build**

```nix
{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage rec {
  pname = "pcmflux";
  version = "2.0.0";
  pyproject = true;
  src = pkgs.fetchFromGitHub {
    owner = "linuxserver"; repo = "pcmflux";
    rev = "ee3d8d3c0e628f7e311b3efc54235c17d018aa5d";
    hash = pkgs.lib.fakeHash;
  };
  cargoDeps = pkgs.rustPlatform.importCargoLock {
    lockFile = ./Cargo.lock;
  };
  nativeBuildInputs = with pkgs; [
    rustPlatform.cargoSetupHook rustc cargo
    python3Packages.setuptools-rust
    cmake pkg-config
  ];
  buildInputs = with pkgs; [ libpulseaudio libopus ];
  pythonImportsCheck = [ "pcmflux" ];
  meta.description = "Selkies PulseAudio capture + Opus encode (Rust/PyO3)";
}
```

- [ ] **Step 3: Build; fill src hash, then cargoDeps hash if prompted; rebuild**

Run: `nix build .#selkies-pcmflux -L`
Expected: hash mismatches to fill (src, and if `importCargoLock` needs it, `outputHashes` for any git dep — pcmflux has none). Then the Rust compile runs.
Expected final: `pythonImportsCheck` passes (`import pcmflux` succeeds).

- [ ] **Step 4: Commit**

```bash
git add nix/packages/selkies-pcmflux/
git commit -m "feat(nix): build pcmflux from source"
```

---

## Task 3: pcmflux wheel (F1 escape hatch)

A parallel, low-risk path so F1 never blocks on the Rust builds. `autoPatchelfHook` over the PyPI manylinux wheel 2.0.0.

**Files:**
- Create: `nix/packages/selkies-pcmflux-wheel/default.nix`

- [ ] **Step 1: Write the wheel derivation**

```nix
{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage {
  pname = "pcmflux";
  version = "2.0.0";
  format = "wheel";
  src = pkgs.python3Packages.fetchPypi {
    pname = "pcmflux"; version = "2.0.0";
    format = "wheel"; dist = "cp312"; python = "cp312";
    abi = "cp312"; platform = "manylinux_2_28_x86_64"; # verified tag (cibuildwheel manylinux_2_28); wheels cp39–cp314
    hash = pkgs.lib.fakeHash;
  };
  nativeBuildInputs = [ pkgs.autoPatchelfHook ];
  buildInputs = with pkgs; [ libpulseaudio libopus stdenv.cc.cc.lib ];
  pythonImportsCheck = [ "pcmflux" ];
}
```

- [ ] **Step 2: Confirm the exact wheel tag on PyPI, fill hash, build**

Run: `curl -s https://pypi.org/pypi/pcmflux/2.0.0/json | ${pkgs.jq}/bin/jq -r '.urls[].filename'` to get the real `cp*`/`manylinux*` tags; adjust `dist/python/abi/platform`.
Run: `nix build .#selkies-pcmflux-wheel -L` → fill hash → rebuild → `pythonImportsCheck` PASS.

- [ ] **Step 3: Commit**

```bash
git add nix/packages/selkies-pcmflux-wheel/
git commit -m "feat(nix): pcmflux manylinux wheel (F1 escape hatch)"
```

---

## Task 4: pixelflux from source (the critical path)

The hard one. Source: `linuxserver/pixelflux` at `9d2caed` (v2.0.0). Has a **committed `Cargo.lock`** recording the `smithay` git dep at rev `ca932e04…` → use `cargoLock.lockFile` + `outputHashes`. Needs libclang (bindgen in `ffmpeg-sys-next`/`x264-sys`), FFmpeg ≤8.1, and the Wayland/DRM/VA stack. This task is where the wheel escape hatch earns its place: if it stalls, F1 proceeds on the wheel (Task 5) while this is finished.

**Files:**
- Read: `.claude/code/pixelflux/{pyproject.toml,setup.py,pixelflux/Cargo.toml,pixelflux/Cargo.lock}`
- Create: `nix/packages/selkies-pixelflux/default.nix`

- [ ] **Step 1: Extract the git-dep outputHashes seed**

From `.claude/code/pixelflux/pixelflux/Cargo.lock`, note the `smithay` `source = "git+…?rev=ca932e04…"` line. `importCargoLock`/`cargoLock` needs an `outputHashes` entry keyed `"smithay-<version>"`.

- [ ] **Step 2: Write the derivation as the failing build**

```nix
{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage rec {
  pname = "pixelflux";
  version = "2.0.0";
  pyproject = true;
  src = pkgs.fetchFromGitHub {
    owner = "linuxserver"; repo = "pixelflux";
    rev = "9d2caedcfe37ffa35f800625e05d0a61ba23af77";
    hash = pkgs.lib.fakeHash;
  };
  cargoDeps = pkgs.rustPlatform.importCargoLock {
    lockFile = "${src}/pixelflux/Cargo.lock";
    outputHashes = {
      "smithay-0.7.0" = pkgs.lib.fakeHash;  # rev ca932e04…; fill exact key+hash from build error
    };
  };
  # The manifest is nested at pixelflux/Cargo.toml, and setuptools-rust runs
  # cargo from that subdir. cargoSetupHook vendors into $CARGO_HOME, but the
  # nested manifest needs cargoRoot so the .cargo config lands where cargo looks.
  cargoRoot = "pixelflux";
  nativeBuildInputs = with pkgs; [
    rustPlatform.cargoSetupHook rustc cargo
    python3Packages.setuptools-rust
    cmake nasm pkg-config
    llvmPackages.libclang
  ];
  buildInputs = with pkgs; [
    ffmpeg  # nixpkgs default is 8.1.2, satisfies ffmpeg-sys-next 8.1's ≤8.1 probe
    x264 libdrm mesa wayland wayland-protocols libinput libxkbcommon libva
  ];
  env.LIBCLANG_PATH = "${pkgs.llvmPackages.libclang.lib}/lib";
  # setuptools-rust builds pixelflux/Cargo.toml; sourceRoot stays repo root.
  pythonImportsCheck = [ "pixelflux" ];
  meta = {
    description = "Selkies Wayland/Smithay capture + FFmpeg h264_vaapi encode (Rust/PyO3)";
    license = with pkgs.lib.licenses; [ mpl20 ]; # note: bundled NVIDIA header is proprietary
  };
}
```

- [ ] **Step 3: Build; iteratively fill src hash, then the smithay `outputHashes` key/hash**

Run: `nix build .#selkies-pixelflux -L`
Expected sequence: (a) src hash mismatch → fill; (b) `importCargoLock` errors naming the exact `smithay-<ver>` key and its `got:` hash → add to `outputHashes`; rebuild.

- [ ] **Step 4: Confirm the FFmpeg version satisfies the probe**

`ffmpeg-sys-next 8.1` probes "up to 8.1". Run: `nix eval --raw nixpkgs#ffmpeg.version` — expected `8.1.2`, a patch of 8.1 that satisfies the crate's major.minor probe, so plain `ffmpeg` is correct. Only if a future nixpkgs bumps to 8.2/9.0 do you pin an older attr via overlay (spec risk 3).

- [ ] **Step 5: Build green**

Run: `nix build .#selkies-pixelflux -L`
Expected: Rust compile completes (smithay, ffmpeg-sys, x264-sys, openh264-sys2 vendored C via cmake/nasm) and `pythonImportsCheck` (`import pixelflux`) PASSES.
If it stalls badly: note the blocker, proceed to Task 5 on the wheel, and return here — F1 is not blocked.

- [ ] **Step 6: Commit**

```bash
git add nix/packages/selkies-pixelflux/default.nix
git commit -m "feat(nix): build pixelflux from source with vendored smithay + VAAPI"
```

---

## Task 5: pixelflux wheel (F1 escape hatch)

Mirror of Task 3 for pixelflux — the manylinux wheel 2.0.0, so F1 can proceed regardless of Task 4.

**Files:**
- Create: `nix/packages/selkies-pixelflux-wheel/default.nix`

- [ ] **Step 1: Write the wheel derivation** (same shape as Task 3, with pixelflux's runtime `buildInputs`: `libva`, `libdrm`, `mesa`, `wayland`, `libxkbcommon`, `libGL`, `stdenv.cc.cc.lib`; NVENC/CUDA are dlopen — do not add).
- [ ] **Step 2: Confirm the PyPI wheel tag, fill hash, build** (`curl … pixelflux/2.0.0/json | jq '.urls[].filename'`).
- [ ] **Step 3: `pythonImportsCheck = [ "pixelflux" ]` PASS.**
- [ ] **Step 4: Commit** `feat(nix): pixelflux manylinux wheel (F1 escape hatch)`

---

## Task 6: Web UI (static `web_root` directory)

At `348bc4f` the web is **served from a `web_root` directory** (`settings.py:130`, default `/opt/selkies-web`), **not** bundled in the wheel — there is no `scripts/ci/build-web.sh` and no `selkies_web` package-data at this commit (that model is `main`-only). Reproduce **LSIO's `Dockerfile` build** instead: build `selkies-web-core`, then `selkies-dashboard` (copying `selkies-core.js` into it), and output the dashboard `dist/` as `$out`. npm lockfiles are gitignored → generate and pin per package.

**Files:**
- Read: `git -C .claude/code/docker-baseimage-selkies show master:Dockerfile` (the web build block, lines ~22-40), `git -C .claude/code/selkies show 348bc4f:addons/selkies-web-core/package.json` and `…:addons/selkies-dashboard/package.json`
- Create: `nix/packages/selkies-web/default.nix`
- Create: the generated `package-lock.json`(s)

- [ ] **Step 1: Generate lockfiles** — in a scratch copy of `addons/selkies-web-core` and `addons/selkies-dashboard`, run `npm install`, save each `package-lock.json` into the package dir.

- [ ] **Step 2: Write the build.** Two npm builds are involved (core, then dashboard which consumes `../selkies-web-core/dist/selkies-core.js`). Cleanest in Nix: a `selkies-web-core` sub-derivation (`buildNpmPackage`) whose `dist/` feeds a `selkies-dashboard` `buildNpmPackage`, each with its own `npmDepsHash`. The dashboard's `postConfigure` copies `${web-core}/selkies-core.js` into `src/`. Output:

```nix
  installPhase = ''
    runHook preInstall
    cp -r dist $out            # the served web_root
    test -f $out/index.html
    runHook postInstall
  '';
```
No `SELKIES_INJECT`, no `__init__.py` — those are the `main` model.

- [ ] **Step 3: Build; fill `npmDepsHash` for each; rebuild** → `result/index.html` exists.

- [ ] **Step 4: Commit** `feat(nix): build the selkies web_root directory`

---

## Task 6b: python-xlib fork

At `348bc4f`, `selkies` depends on `python-xlib @ https://github.com/selkies-project/python-xlib/archive/master.zip` — the **selkies fork**, not PyPI's `xlib`. Package it so the wheel (Task 7) can take it as a normal input.

**Files:**
- Create: `nix/packages/selkies-python-xlib/default.nix`

- [ ] **Step 1: Write the derivation**

```nix
{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage {
  pname = "python-xlib-selkies";
  version = "0-unstable";
  pyproject = true;
  src = pkgs.fetchFromGitHub {
    owner = "selkies-project"; repo = "python-xlib";
    rev = "master";              # pin to a concrete rev in Step 2
    hash = pkgs.lib.fakeHash;
  };
  build-system = [ pkgs.python3Packages.setuptools ];
  pythonImportsCheck = [ "Xlib" ];
}
```

- [ ] **Step 2: Pin a concrete rev, fill hash, build.** Resolve `master` to a commit SHA (`git ls-remote https://github.com/selkies-project/python-xlib master`), set it as `rev`, fill the hash → `import Xlib` PASS.

- [ ] **Step 3: Commit** `feat(nix): package the selkies python-xlib fork`

---

## Task 7: The `selkies` wheel (assembly)

The pure-Python wheel, taking pixelflux/pcmflux as arguments so F1 (wheels) and F2 (from source) swap by argument. Source: `selkies` at `348bc4f`. **Exact external deps at that commit** (from `git show 348bc4f:pyproject.toml`): `websockets`, `gputil`, `prometheus_client`, `msgpack`, `pynput`, `psutil`, `watchdog`, `Pillow`, `python-xlib` (the fork, Task 6b), `xkbcommon`, `distro`, `pulsectl`, `pasimple`, `aioice`, `av`, `cffi`, `cryptography>=44`, `google-crc32c`, `pyee`, `pylibsrtp`, `pyopenssl>=25`, `aiohttp`, `aiofiles`, plus `pixelflux`/`pcmflux`. **`webrtc` is vendored — do NOT add aiortc.** The web UI is NOT bundled here (Task 6 produces the `web_root`; #2 wires `--web_root`).

**Files:**
- Read: `git -C .claude/code/selkies show 348bc4f:pyproject.toml`
- Create: `nix/packages/selkies/default.nix`

- [ ] **Step 1: Write the derivation with pixelflux/pcmflux as overridable args**

```nix
{ pkgs, perSystem, ... }:
let
  py = pkgs.python3Packages;
  pixelflux = perSystem.self.selkies-pixelflux-wheel;   # F1 default; F2 swaps to selkies-pixelflux
  pcmflux   = perSystem.self.selkies-pcmflux-wheel;
  pythonXlib = perSystem.self.selkies-python-xlib;
in
py.buildPythonPackage rec {
  pname = "selkies";
  version = "0.0.0.dev0-348bc4f";
  pyproject = true;
  src = pkgs.fetchFromGitHub {
    owner = "selkies-project"; repo = "selkies";
    rev = "348bc4f61da66198573e7e57db9a266aca1991d5";
    hash = pkgs.lib.fakeHash;
  };
  # Drop the python-xlib URL form; it's satisfied from propagatedBuildInputs.
  postPatch = ''
    substituteInPlace pyproject.toml \
      --replace-fail '"python-xlib @ https://github.com/selkies-project/python-xlib/archive/master.zip",' ""
  '';
  propagatedBuildInputs = [ pixelflux pcmflux pythonXlib ] ++ (with py; [
    websockets gputil prometheus-client msgpack pynput psutil watchdog pillow
    xkbcommon distro pulsectl pasimple aioice av cffi cryptography
    google-crc32c pyee pylibsrtp pyopenssl aiohttp aiofiles
  ]);
  pythonImportsCheck = [ "selkies" ];
}
```

- [ ] **Step 2: Build; fill src hash; resolve nixpkgs attr names**

Run: `nix build .#selkies -L`
Attr names verified present in nixpkgs (use these): `gputil` (lowercase — `GPUtil` does not exist), `pasimple` (0.0.2), `pulsectl` (24.12), `xkbcommon` (1.5.1), `distro` (1.9), `pynput` (1.8.2), `websockets`, `prometheus-client` (not `prometheus_client`). Floors met: `cryptography` 49 ≥44 ✓, `pyopenssl` 26.3 ≥25 ✓, `pylibsrtp` 1.0 ≥0.10 ✓, `pyee` 13 ≥13 ✓.

- [ ] **Step 3: Verify the CLI and the forked Xlib**

```nix
  doInstallCheck = true;
  installCheckPhase = ''
    $out/bin/selkies --help >/dev/null
    ${py.python.interpreter} -c "import selkies, Xlib"
  '';
```
Run: `nix build .#selkies -L` → PASS.

- [ ] **Step 4: Commit** `feat(nix): assemble the selkies wheel (F1, on wheels)`

---

## Task 8: F1 end-to-end smoke

Prove the whole chain with software encode over WebSocket, before touching VAAPI. This runs on rhea (the AMD box) or any Linux host with `/dev/dri`.

**Files:**
- Create: `nix/checks/selkies-smoke.nix` (or a `scripts/selkies-smoke.sh` run manually)

- [ ] **Step 1: Write the smoke as a script**

A script that: runs `selkies --web_root ${selkies-web}` with `PIXELFLUX_WAYLAND=true` and software encode, launches a throwaway Wayland client (`${pkgs.mesa-demos}/bin/eglgears_wayland` or `kmscube`) against the published `WAYLAND_DISPLAY`, and curls the Selkies HTTP port for a 200 + `index.html`. **First confirm the exact software-encode flags at `348bc4f`**: `git -C .claude/code/selkies show 348bc4f:src/selkies/settings.py | grep -iE 'use_cpu|encoder|framerate'` — the reconnaissance names (`SELKIES_USE_CPU`, `SELKIES_ENCODER=jpeg`) were read from `main`; use whatever `settings.py` at `348bc4f` actually exposes.

- [ ] **Step 2: Run it on rhea**

Expected: Selkies starts, publishes a `WAYLAND_DISPLAY`, the demo client renders, and the port serves `index.html`. Capture a browser screenshot of the stream tab as evidence (@superpowers:verification-before-completion).

- [ ] **Step 3: Commit** `test(nix): F1 end-to-end selkies smoke (software encode)`

**F1 milestone reached:** a working, browser-embeddable stream on wheels + software encode.

---

## Task 9 (F2): Switch to from-source and turn on VAAPI

- [ ] **Step 1: Point the `selkies` package at the from-source derivations**

In `nix/packages/selkies/default.nix`, swap `selkies-pixelflux-wheel`/`selkies-pcmflux-wheel` → `selkies-pixelflux`/`selkies-pcmflux`. Run: `nix build .#selkies -L` → green.

- [ ] **Step 2: Enable VAAPI and verify on rhea**

Re-run the smoke with `SELKIES_USE_CPU=false` / `SELKIES_ENCODER` default (VAAPI). Expected: `vainfo` shows `VAProfileH264*` enc; the container log shows the VAAPI encoder init and does NOT fall back to JPEG. Confirm zero-copy dmabuf path (no per-frame CPU copy in logs).

- [ ] **Step 3: Commit** `feat(nix): F2 — selkies on from-source pixelflux/pcmflux with VAAPI`

**F2 milestone reached:** from-source stack with hardware H.264.

---

## Out of scope (future plans)

- **F3 / WebRTC** (`--mode=webrtc`, signaling plane, possible bump to `main`) — its own spec + plan.
- **Sub-project #2**: the `system-manager` module, systemd units, GPU driver env on Debian, the broker's native env adaptation, Flycast wiring, TLS/reverse-proxy. Depends on this plan's packages existing.
