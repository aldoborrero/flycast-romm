{ pkgs, ... }:
pkgs.stdenv.mkDerivation (finalAttrs: {
  pname = "selkies-js-interposer";
  version = "0-unstable-348bc4f";

  src = pkgs.fetchFromGitHub {
    owner = "selkies-project";
    repo = "selkies";
    rev = "348bc4f61da66198573e7e57db9a266aca1991d5";
    hash = "sha256-buiWdWvweSIGG/N9QRBkxlBcXvPbFjNIC6zyZydpYuc=";
  };

  sourceRoot = "${finalAttrs.src.name}/addons/js-interposer";

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

  doInstallCheck = true;
  installCheckPhase = ''
    test -f $out/lib/selkies_joystick_interposer.so
    ${pkgs.file}/bin/file $out/lib/selkies_joystick_interposer.so | grep -q "shared object"
  '';

  meta = {
    description = "LD_PRELOAD shim mapping browser gamepads to /dev/input/js*";
    license = pkgs.lib.licenses.mpl20;
  };
})
