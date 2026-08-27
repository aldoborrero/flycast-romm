{ pkgs, ... }:
let
  inherit (pkgs)
    lib
    stdenv
    fetchFromGitHub
    fetchurl
    importNpmLock
    nodejs
    ;

  # Selkies source at the commit LSIO pins.
  selkiesSrc = fetchFromGitHub {
    owner = "selkies-project";
    repo = "selkies";
    rev = "348bc4f61da66198573e7e57db9a266aca1991d5";
    hash = "sha256-buiWdWvweSIGG/N9QRBkxlBcXvPbFjNIC6zyZydpYuc=";
  };

  # SDL_GameControllerDB — fetched once at eval time so gendb.js can run
  # offline inside the Nix sandbox.  The URL inside gendb.js is hardcoded to
  # `master`; we pin by hash to keep builds reproducible.
  sdlGameControllerDB = fetchurl {
    url = "https://raw.githubusercontent.com/mdqinc/SDL_GameControllerDB/master/gamecontrollerdb.txt";
    hash = "sha256-rqEVIPwTg8nuTW8dP5H9Xc6B8aZuks7wBIUGXmwibwY=";
  };

  # ---------------------------------------------------------------------------
  # selkies-web-core node_modules (offline, via importNpmLock)
  # ---------------------------------------------------------------------------
  webCoreNodeModules = importNpmLock.buildNodeModules {
    npmRoot = lib.cleanSource (selkiesSrc + "/addons/selkies-web-core");
    package = lib.importJSON ./web-core-package.json;
    packageLock = lib.importJSON ./web-core-package-lock.json;
    nodejs = nodejs;
    derivationArgs = {
      pname = "selkies-web-core-node-modules";
      version = "1.0.0";
    };
  };

  # ---------------------------------------------------------------------------
  # selkies-web-core build — produces dist/selkies-core.js + dist/jsdb/
  # ---------------------------------------------------------------------------
  webCore = stdenv.mkDerivation {
    pname = "selkies-web-core";
    version = "1.0.0";

    src = selkiesSrc + "/addons/selkies-web-core";

    nativeBuildInputs = [
      nodejs
      importNpmLock.hooks.linkNodeModulesHook
    ];

    npmDeps = webCoreNodeModules;

    postPatch = ''
      # Patch gendb.js to read the SDL controller DB from the Nix store
      # instead of fetching it from the network (the sandbox blocks outbound
      # connections).  The function contract stays identical: same parsing
      # logic, same output format, just a local file source.
      substituteInPlace gendb.js \
        --replace-fail "const response = await fetch(DB_URL);" \
          "const response = { ok: true, text: async () => require('fs').readFileSync('${sdlGameControllerDB}', 'utf8') };" \
        --replace-fail "if (!response.ok) {" \
          "if (false) {" \
        --replace-fail "throw new Error(\`Failed to fetch: \''${response.status} \''${response.statusText}\`);" \
          ""
    '';

    buildPhase = ''
      runHook preBuild

      # vite build generates dist/selkies-core.js (and workers).
      # node gendb.js is the postbuild hook — we run it manually so we can
      # pass the patched script that reads the pre-fetched SDL DB.
      ./node_modules/.bin/vite build
      node gendb.js

      runHook postBuild
    '';

    installPhase = ''
      runHook preInstall
      cp -r dist $out
      runHook postInstall
    '';
  };

  # ---------------------------------------------------------------------------
  # selkies-dashboard node_modules (offline, via importNpmLock)
  # ---------------------------------------------------------------------------
  dashboardNodeModules = importNpmLock.buildNodeModules {
    npmRoot = lib.cleanSource (selkiesSrc + "/addons/selkies-dashboard");
    package = lib.importJSON ./dashboard-package.json;
    packageLock = lib.importJSON ./dashboard-package-lock.json;
    nodejs = nodejs;
    derivationArgs = {
      pname = "selkies-dashboard-node-modules";
      version = "0.0.0";
    };
  };

in
# ---------------------------------------------------------------------------
# selkies-dashboard — the top-level derivation; $out is the served web_root
# ---------------------------------------------------------------------------
stdenv.mkDerivation {
  pname = "selkies-web";
  version = "0-unstable-2024-07-02"; # approximate date of commit 348bc4f

  src = selkiesSrc + "/addons/selkies-dashboard";

  nativeBuildInputs = [
    nodejs
    importNpmLock.hooks.linkNodeModulesHook
  ];

  npmDeps = dashboardNodeModules;

  preBuild = ''
    # Replicate the LSIO recipe: copy selkies-core.js into src/ before the
    # dashboard Vite build, so the import in src/main.jsx resolves locally.
    cp ${webCore}/selkies-core.js src/selkies-core.js
  '';

  buildPhase = ''
    runHook preBuild
    ./node_modules/.bin/vite build
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall

    # The Nix output is the web_root that selkies --web_root points at.
    # Replicate the post-build file layout from the LSIO Dockerfile:
    #   dist/src/selkies-core.js      (runtime JS loaded by the dashboard HTML)
    #   dist/src/universalTouchGamepad.js
    #   dist/nginx/{header,footer}.html
    #   dist/jsdb/*.json              (SDL gamepad mappings, from gendb.js)
    mkdir -p dist/src dist/nginx
    cp ${webCore}/selkies-core.js dist/src/
    cp ${selkiesSrc}/addons/universal-touch-gamepad/universalTouchGamepad.js dist/src/
    cp ${selkiesSrc}/addons/selkies-web-core/nginx/header.html dist/nginx/
    cp ${selkiesSrc}/addons/selkies-web-core/nginx/footer.html dist/nginx/
    cp -r ${webCore}/jsdb dist/

    cp -r dist $out
    test -f $out/index.html

    runHook postInstall
  '';

  meta = {
    description = "Selkies web UI static assets (web_root directory for selkies --web_root)";
    license = lib.licenses.gpl3Only;
    platforms = lib.platforms.linux;
  };
}
