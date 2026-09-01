{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage rec {
  pname = "pixelflux";
  version = "2.0.0";
  pyproject = true;

  src = pkgs.fetchFromGitHub {
    owner = "linuxserver";
    repo = "pixelflux";
    rev = "9d2caedcfe37ffa35f800625e05d0a61ba23af77";
    hash = "sha256-LSZ7p6tnT6bWJ34jyIJ0LBGdY+PmndfP91l1a7hu36E=";
  };

  # Drain Smithay's deferred texture-destruction queue while capture is stopped.
  # When no WebRTC client is connected the render tick early-outs without
  # finishing a Frame, so imported GPU buffers from the still-rendering nested
  # clients (Flycast's idle game list) are never released until the next
  # start_capture() — an idle session grows unbounded. On the AMD iGPU these
  # buffers are system RAM, so it surfaces as RSS growth and OOM.
  # Upstream bug with no merged fix yet: linuxserver/pixelflux#26 (smithay#1747).
  patches = [ ./wayland-idle-texture-leak.patch ];

  # The Rust manifest lives under pixelflux/ (nested), so cargoSetupHook and
  # setuptools-rust are both pointed at that subdir. Upstream commits its
  # Cargo.lock, so we vendor straight from it — no generation or postPatch.
  cargoRoot = "pixelflux";
  cargoDeps = pkgs.rustPlatform.importCargoLock {
    lockFile = "${src}/pixelflux/Cargo.lock";
    outputHashes = {
      # smithay is pulled from git (rev ca932e04…); importCargoLock can't infer
      # a registry hash for a git dep, so we pin the vendored tree hash here.
      "smithay-0.7.0" = "sha256-Baga3bncPoceJaUqzQ4scxZTeOflvXIuSiYn8DBCY9Q=";
    };
  };

  nativeBuildInputs = with pkgs; [
    rustPlatform.cargoSetupHook
    # bindgenHook sets LIBCLANG_PATH *and* the BINDGEN_EXTRA_CLANG_ARGS that
    # point clang at the C stdlib headers (inttypes.h/stdint.h) and its resource
    # dir. ffmpeg-sys-next and x264-sys run bindgen; without this their build.rs
    # panics with "'inttypes.h' file not found".
    rustPlatform.bindgenHook
    rustc
    cargo
    python3Packages.setuptools-rust
    cmake
    nasm
    pkg-config
  ];

  buildInputs = with pkgs; [
    # ffmpeg-sys-next 8.1 probes the system FFmpeg via pkg-config and enables the
    # version-gated cfgs up to the release it is named after. The flake's pinned
    # nixpkgs currently ships FFmpeg 9.0 (newer than the crate's 8.1 ceiling); the
    # crate treats "> 8.1" as "has every API it knows" and builds clean, and
    # pixelflux only touches 5.1-era avcodec/avfilter for h264_vaapi, so the link
    # (libavcodec.so.63) and `import pixelflux` both succeed. If a future crate
    # bump or an FFmpeg 9.x ABI change breaks this, pin an 8.1 ffmpeg attr here.
    ffmpeg
    x264
    libdrm
    # GBM (smithay backend_gbm + the gbm crate) and EGL/GL (backend_egl,
    # renderer_gl) — libgbm is mesa's GBM output, libGL provides libEGL/libGL.
    libgbm
    libGL
    # renderer_pixman
    pixman
    wayland
    wayland-protocols
    libinput
    libxkbcommon
    libva
    # libinput's backend_udev needs libudev (provided by systemd's lib output).
    udev
  ];

  # cmake/nasm are here for the vendored C builds inside turbojpeg-sys and
  # openh264-sys2 build scripts, not for the top-level project — suppress
  # stdenv's cmake configure hook so it doesn't fire on the non-cmake package.
  dontUseCmakeConfigure = true;

  pythonImportsCheck = [ "pixelflux" ];

  meta = {
    description = "Selkies Wayland/Smithay capture + FFmpeg h264_vaapi encode (Rust/PyO3)";
    # MPL-2.0 for pixelflux itself; the bundled nvcodec-sys headers
    # (nvEncodeAPI.h) are proprietary NVIDIA, compiled only as committed
    # bindings (no NVIDIA build/link deps here).
    # from-source links GPL x264 (x264-sys); the binary is effectively GPL
    license = with pkgs.lib.licenses; [
      mpl20
      gpl2Plus
    ];
  };
}
