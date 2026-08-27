{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage {
  pname = "pcmflux";
  version = "2.0.0";
  format = "wheel";

  src = pkgs.python3Packages.fetchPypi {
    pname = "pcmflux";
    version = "2.0.0";
    format = "wheel";
    dist = "cp314";
    python = "cp314";
    abi = "cp314";
    platform = "manylinux_2_28_x86_64";
    hash = "sha256-BQqMfXwJnuNjJ58IXKtl5fmJHcarlIGj7fZKkB0nY1I=";
  };

  nativeBuildInputs = [ pkgs.autoPatchelfHook ];

  buildInputs = with pkgs; [
    libpulseaudio
    libopus
    stdenv.cc.cc.lib
    libx11
    libxext
    libice
    libsm
  ];

  pythonImportsCheck = [ "pcmflux" ];

  meta = {
    description = "Selkies pcmflux (prebuilt manylinux wheel)";
    license = pkgs.lib.licenses.mpl20;
  };
}
