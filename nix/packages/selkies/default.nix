{ pkgs, perSystem, ... }:
let
  py = pkgs.python3Packages;
  pixelflux  = perSystem.self.selkies-pixelflux-wheel;
  pcmflux    = perSystem.self.selkies-pcmflux-wheel;
  pythonXlib = perSystem.self.selkies-python-xlib;
  # pynput propagates the vanilla python-xlib; override it to use the
  # selkies fork so there is only one python_xlib in the closure.
  pynput = py.pynput.override { python-xlib = pythonXlib; };
in
py.buildPythonPackage rec {
  pname = "selkies";
  version = "0.0.0";
  pyproject = true;

  src = pkgs.fetchFromGitHub {
    owner = "selkies-project";
    repo = "selkies";
    rev = "348bc4f61da66198573e7e57db9a266aca1991d5";
    hash = "sha256-buiWdWvweSIGG/N9QRBkxlBcXvPbFjNIC6zyZydpYuc=";
  };

  # Drop the python-xlib URL form; satisfied from propagatedBuildInputs via
  # the selkies-project fork (selkies-python-xlib).
  postPatch = ''
    substituteInPlace pyproject.toml \
      --replace-fail '    "python-xlib @ https://github.com/selkies-project/python-xlib/archive/master.zip",' ""
  '';

  build-system = [ py.setuptools py.wheel ];

  propagatedBuildInputs = [ pixelflux pcmflux pythonXlib pynput ] ++ (with py; [
    websockets
    gputil
    prometheus-client
    msgpack
    psutil
    watchdog
    pillow
    xkbcommon
    distro
    pulsectl
    pasimple
    aioice
    av
    cffi
    cryptography
    google-crc32c
    pyee
    pylibsrtp
    pyopenssl
    aiohttp
    aiofiles
  ]);

  pythonImportsCheck = [ "selkies" ];

  doInstallCheck = true;
  installCheckPhase = ''
    ${py.python.interpreter} -c "import selkies, Xlib"
  '';

  meta.description = "Selkies streaming server (348bc4f), on prebuilt pixelflux/pcmflux wheels";
}
