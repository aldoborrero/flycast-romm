{ pkgs, ... }:
pkgs.python3Packages.buildPythonPackage {
  pname = "python-xlib-selkies";
  version = "0-unstable-2026-08-27";

  # No pyproject.toml — upstream uses setup.py + setup.cfg (setuptools).
  format = "setuptools";

  src = pkgs.fetchFromGitHub {
    owner = "selkies-project";
    repo = "python-xlib";
    rev = "932e5d18c3edcb6a02e11c5e0b31c0f0ce4fd571";
    hash = "sha256-6Fl8qcxvbPlMfRs1yxQbGPc3Mh0XibsaAEOuKewrUJ4=";
  };

  propagatedBuildInputs = [ pkgs.python3Packages.six ];

  pythonImportsCheck = [ "Xlib" ];

  meta.description = "Selkies fork of python-xlib";
}
