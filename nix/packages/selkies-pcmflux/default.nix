{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage {
  pname = "pcmflux";
  version = "2.0.0";
  pyproject = true;

  src = pkgs.fetchFromGitHub {
    owner = "linuxserver";
    repo = "pcmflux";
    rev = "ee3d8d3c0e628f7e311b3efc54235c17d018aa5d";
    hash = "sha256-vNUi0FXnec68/uWPyPr1N+Qknejr4NlbNfrfoJb1SdU=";
  };

  # The Cargo.toml lives under pcmflux/ (nested), so we tell cargoSetupHook
  # where to look. The upstream ships no Cargo.lock, so we inject our
  # generated one via postPatch — cargoSetupPostPatchHook then validates it
  # against the vendor dir produced by importCargoLock.
  cargoRoot = "pcmflux";
  cargoDeps = pkgs.rustPlatform.importCargoLock {
    lockFile = ./Cargo.lock;
  };

  postPatch = ''
    cp ${./Cargo.lock} pcmflux/Cargo.lock
  '';

  nativeBuildInputs = with pkgs; [
    rustPlatform.cargoSetupHook
    rustc
    cargo
    python3Packages.setuptools-rust
    cmake
    pkg-config
  ];

  buildInputs = with pkgs; [
    libpulseaudio
    libopus
  ];

  # cmake is in nativeBuildInputs for audiopus_sys's build script, not for the
  # top-level package — suppress the cmake configure hook.
  dontUseCmakeConfigure = true;

  pythonImportsCheck = [ "pcmflux" ];

  meta = {
    description = "Selkies PulseAudio capture + Opus encode (Rust/PyO3)";
    license = pkgs.lib.licenses.mpl20;
  };
}
