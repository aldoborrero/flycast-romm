{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage {
  pname = "pixelflux";
  version = "2.0.0";
  format = "wheel";

  src = pkgs.python3Packages.fetchPypi {
    pname = "pixelflux";
    version = "2.0.0";
    format = "wheel";
    dist = "cp314";
    python = "cp314";
    abi = "cp314";
    platform = "manylinux_2_28_x86_64";
    hash = "sha256-fFAdGE+6eXRspjEYtcxGKFG3ytIXCU4RoBFpazv6OVM=";
  };

  nativeBuildInputs = [ pkgs.autoPatchelfHook ];

  buildInputs = with pkgs; [
    libva
    libdrm
    mesa
    wayland
    libxkbcommon
    libGL
    pixman
    stdenv.cc.cc.lib
  ];

  pythonImportsCheck = [ "pixelflux" ];

  meta = {
    description = "Selkies pixelflux (prebuilt manylinux wheel)";
    # prebuilt wheel bundles x264 (GPL)
    license = pkgs.lib.licenses.mpl20;
  };
}
