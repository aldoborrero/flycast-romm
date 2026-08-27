{ pkgs, ... }:
pkgs.stdenv.mkDerivation {
  pname = "selkies-js-interposer";
  version = "0.0.0-stub";
  dontUnpack = true;
  installPhase = "mkdir -p $out";
  meta.description = "stub, to be implemented";
}
