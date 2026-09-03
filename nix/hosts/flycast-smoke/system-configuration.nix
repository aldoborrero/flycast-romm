#
# flycast-smoke LXC (CT 1103 on rhea): a bare Debian container with Nix on top.
# system-manager owns the whole streaming stack as systemd services so the
# launch is reproducible from git instead of hand-run `systemd-run` one-shots.
#
# Stack, in start order:
#   flycast-xdg   oneshot: create the XDG_RUNTIME_DIR the rest share
#   flycast-pulse pulseaudio with a null sink (selkies/pcmflux captures its monitor)
#   flycast-selkies the PATCHED selkies (source-built pixelflux, leak fix #26).
#                  pixelflux is ALSO the headless-GPU Wayland compositor (wayland-1);
#                  flycast renders into it. PIXELFLUX_WAYLAND=true so it takes the
#                  Wayland path and never shells out to xrandr (X11 path aborts video)
#   flycast-broker the RomM broker; spawns flycast per /launch request
#   flycast-caddy  TLS front on :8443, serves the web UI and proxies the ws to selkies
#
{
  pkgs,
  lib,
  perSystem,
  flake,
  ...
}:
let
  selkies = perSystem.self.selkies;
  selkiesWeb = perSystem.self.selkies-web;
  interposer = perSystem.self.selkies-js-interposer;
  broker = perSystem.self.default;
  flycast = pkgs.flycast;

  # The broker forwards its own environment to the flycast child. It cannot
  # carry LD_LIBRARY_PATH: the child is launched through the container's Debian
  # sudo, and a nix LD_LIBRARY_PATH makes sudo's own loader pull nix's libdl
  # against Debian's libc and die. So flycast gets its GPU libs from this thin
  # wrapper instead, set after sudo has already run.
  flycastWrapped = pkgs.writeShellScript "flycast-wrapped" ''
    export LD_LIBRARY_PATH="${glLibPath}''${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
    export LD_PRELOAD="${interposer}/lib/selkies_joystick_interposer.so"
    exec ${flycast}/bin/flycast "$@"
  '';

  # The container's LAN address (DHCP reservation on rhea's vmbr0). Caddy's
  # internal CA mints the cert for this exact host; a port-only `:8443` site has
  # no default cert, so an IP request with no SNI fails the TLS handshake.
  hostIp = "192.168.120.216";

  runtimeDir = "/run/xdg";
  # pixelflux is itself the (headless-GPU, on renderD128) Wayland compositor;
  # selkies calls ensure_wayland_display and, on a clean XDG_RUNTIME_DIR, it
  # lands on wayland-1. flycast must render into THAT. The original setup ran a
  # separate weston that stole wayland-1, bumping pixelflux to wayland-2 while
  # flycast rendered into the (uncaptured) weston — hence a black stream.
  waylandDisplay = "wayland-1";
  driNode = "/dev/dri/renderD128";
  romRoot = "/romm/library/roms";
  flycastHome = "/root";
  flycastConfigDir = "${flycastHome}/.config/flycast";

  # GPU + Wayland runtime environment shared by weston, selkies and the broker.
  # The AMD 780M is driven by radeonsi/RADV; the loaders are found through mesa.
  glLibPath = lib.makeLibraryPath [
    pkgs.wayland
    pkgs.vulkan-loader
    pkgs.libglvnd
    pkgs.mesa
  ];
  glEnv = {
    XDG_RUNTIME_DIR = runtimeDir;
    LD_LIBRARY_PATH = glLibPath;
    LIBGL_DRIVERS_PATH = "${pkgs.mesa}/lib/dri";
    GBM_BACKENDS_PATH = "${pkgs.mesa}/lib/gbm";
    __EGL_VENDOR_LIBRARY_DIRS = "${pkgs.mesa}/share/glvnd/egl_vendor.d";
    VK_ICD_FILENAMES = "${pkgs.mesa}/share/vulkan/icd.d/radeon_icd.x86_64.json";
    LIBVA_DRIVERS_PATH = "${pkgs.mesa}/lib/dri";
    LIBVA_DRIVER_NAME = "radeonsi";
  };

  # Caddy serves the selkies web UI over self-signed TLS and reverse-proxies the
  # WebSocket upgrade to selkies on :8080. Bound to :8443 for any host so it
  # survives the container's DHCP lease changing.
  caddyfile = pkgs.writeText "flycast-caddyfile" ''
    {
      admin off
      auto_https disable_redirects
    }
    https://${hostIp}:8443 {
      tls internal
      @ws {
        header Connection *Upgrade*
        header Upgrade websocket
      }
      reverse_proxy @ws 127.0.0.1:8080
      root * ${selkiesWeb}
      file_server
      try_files {path} /index.html
    }
  '';

  mkService = extra: lib.recursiveUpdate {
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Restart = "on-failure";
      RestartSec = 2;
    };
  } extra;
in
{
  config = {
    nixpkgs.hostPlatform = "x86_64-linux";

    # On PATH for the whole system: clipboard helpers selkies shells out to (so
    # its clipboard thread does not spin on FileNotFoundError), plus the tools
    # handy for poking at the stack over SSH.
    environment.systemPackages = with pkgs; [
      xclip
      wl-clipboard
      pulseaudio
      caddy
      flycast
    ];

    systemd.services = {
      # Shared XDG_RUNTIME_DIR (weston's wayland socket, pulse's socket).
      flycast-xdg = mkService {
        description = "flycast-smoke: create shared XDG runtime dir";
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = pkgs.writeShellScript "flycast-xdg" ''
            mkdir -p ${runtimeDir} ${runtimeDir}/pulse
            chmod 700 ${runtimeDir}
          '';
          Restart = lib.mkForce "no";
        };
      };

      flycast-pulse = mkService {
        description = "flycast-smoke: pulseaudio with a null sink";
        after = [ "flycast-xdg.service" ];
        requires = [ "flycast-xdg.service" ];
        environment = {
          HOME = flycastHome;
          PULSE_RUNTIME_PATH = "${runtimeDir}/pulse";
        };
        serviceConfig.ExecStart = ''
          ${pkgs.pulseaudio}/bin/pulseaudio -n --exit-idle-time=-1 --disallow-exit=true \
            --load=module-native-protocol-unix \
            --load='module-null-sink sink_name=null' \
            --load=module-always-sink
        '';
      };

      flycast-selkies = mkService {
        description = "flycast-smoke: selkies streaming + pixelflux compositor (leak fix #26)";
        after = [ "flycast-xdg.service" "flycast-pulse.service" ];
        requires = [ "flycast-xdg.service" ];
        path = [ pkgs.xclip pkgs.wl-clipboard ];
        environment = glEnv // {
          HOME = flycastHome;
          # Take the Wayland capture path; without this selkies uses xrandr to
          # find the screen and aborts the video pipeline on this headless host.
          PIXELFLUX_WAYLAND = "true";
          PULSE_RUNTIME_PATH = "${runtimeDir}/pulse";
        };
        serviceConfig.ExecStart = ''
          ${selkies}/bin/selkies --mode websockets --port 8080 \
            --web-root ${selkiesWeb} --dri-node ${driNode} \
            --audio-enabled true --gamepad-enabled true \
            --audio-device-name null.monitor
        '';
      };

      flycast-broker = mkService {
        description = "flycast-smoke: RomM streaming broker";
        # flycast connects to pixelflux's compositor, so selkies must be up first.
        after = [ "flycast-selkies.service" "flycast-pulse.service" ];
        requires = [ "flycast-selkies.service" ];
        # LD_LIBRARY_PATH is deliberately dropped here (see flycastWrapped); with
        # it set, the Debian sudo the broker shells out to fails to load.
        environment = (removeAttrs glEnv [ "LD_LIBRARY_PATH" ]) // {
          # The broker launches flycast through `sudo -u <user> env … flycast`.
          # Use the container's own Debian sudo/env (/usr/bin) — nixpkgs sudo
          # cannot parse Debian's @include PAM stack and dies "no modules
          # loaded", so flycast never starts.
          PATH = lib.mkForce "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin";
          HOME = flycastHome;
          FLYCAST_HOME = flycastHome;
          FLYCAST_USER = "root";
          FLYCAST_BIN = "${flycastWrapped}";
          FLYCAST_CONFIG_DIR = flycastConfigDir;
          BROKER_PORT = "8000";
          BROKER_LOG_LEVEL = "INFO";
          ROM_ROOT = romRoot;
          WAYLAND_DISPLAY = waylandDisplay;
          SDL_VIDEODRIVER = "wayland";
          SDL_AUDIODRIVER = "pulseaudio";
          PULSE_RUNTIME_PATH = "${runtimeDir}/pulse";
          # Point SDL at the fixed device path the selkies joystick interposer
          # (LD_PRELOAD in flycastWrapped) intercepts — otherwise SDL enumerates
          # nothing in the container and browser gamepads never reach flycast.
          SDL_JOYSTICK_DEVICE = "/dev/input/js0";
        };
        serviceConfig = {
          ExecStartPre = pkgs.writeShellScript "flycast-broker-pre" ''
            # flycast loads romm-broker.lua from its config dir; ship it there.
            ${pkgs.coreutils}/bin/install -Dm644 \
              ${flake}/lua/romm-broker.lua ${flycastConfigDir}/romm-broker.lua

            # Force the Vulkan renderer. flycast's OpenGL path leaks host RAM
            # (~700-1100 MiB/h, radeonsi/Mesa on the 780M; Vulkan is flat with
            # the same games). emu.cfg is mutable runtime state that flycast
            # rewrites, so enforce pvr.rend on every start rather than trusting
            # its persistence.
            cfg=${flycastConfigDir}/emu.cfg
            if [ -f "$cfg" ] && ${pkgs.gnugrep}/bin/grep -q '^pvr\.rend = ' "$cfg"; then
              ${pkgs.gnused}/bin/sed -i 's/^pvr\.rend = .*/pvr.rend = 4/' "$cfg"
            else
              ${pkgs.coreutils}/bin/mkdir -p ${flycastConfigDir}
              printf '[config]\npvr.rend = 4\n' >> "$cfg"
            fi
          '';
          ExecStart = "${broker}/bin/romm-broker";
        };
      };

      flycast-caddy = mkService {
        description = "flycast-smoke: caddy TLS front for the web UI";
        after = [ "flycast-selkies.service" ];
        environment = {
          HOME = flycastHome;
          XDG_DATA_HOME = "${flycastHome}/.local/share";
        };
        serviceConfig.ExecStart = ''
          ${pkgs.caddy}/bin/caddy run --config ${caddyfile} --adapter caddyfile
        '';
      };
    };
  };
}
