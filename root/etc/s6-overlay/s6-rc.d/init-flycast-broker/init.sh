#!/usr/bin/with-contenv bash
# Prepare the container for the broker. Everything here is fast and offline:
# the whole service stack waits behind this oneshot (see the init-services
# dependency), so a slow step would stall the stream.
set -u

say() { echo "[flycast-broker-mod] $*"; }

CONFIG_DIR="${FLYCAST_CONFIG_DIR:-/config/.config/flycast}"
CHANNEL_DIR="${CONFIG_DIR}/romm-broker"

# Without PUID the linuxserver init leaves abc at its baked-in uid (911, not
# 1000), so the fallback is abc's real ids — files chowned to the wrong id
# wedge the lua channel with no visible error. The broker applies the same
# fallback for the files it creates itself.
PUID="${PUID:-$(id -u abc 2>/dev/null || echo 1000)}"
PGID="${PGID:-$(id -g abc 2>/dev/null || echo 1000)}"

# ── Stop the desktop launching its own Flycast ───────────────────────────────
# The base image copies /defaults/autostart_wayland to ~/.config/labwc/autostart
# on first run, and labwc executes it. The broker owns the emulator's lifecycle
# — it has to know the PID to supervise it, and to be able to kill it for a
# launch — so the desktop must not start a second one.
#
# Both window managers are handled: the flycast image sets PIXELFLUX_WAYLAND
# and runs labwc, but the same base image has an Xorg/openbox path.
for wm in labwc openbox; do
  autostart="/config/.config/${wm}/autostart"
  [ -d "$(dirname "${autostart}")" ] || continue
  if ! grep -qs 'flycast-broker-mod' "${autostart}"; then
    printf '# Disabled by flycast-broker-mod: the broker owns the emulator process.\n' >"${autostart}"
    chown "${PUID}:${PGID}" "${autostart}" 2>/dev/null || true
    say "Disabled the ${wm} autostart."
  fi
done

# RESTART_APP starts a watchdog that relaunches the autostart script and locks
# it to root:abc 0550. With the autostart neutered the watchdog has nothing
# useful to restart, and it fights the broker for the process.
if [ "${RESTART_APP,,}" = "true" ]; then
  say "WARNING: RESTART_APP is true. Its watchdog competes with the broker for the emulator process; unset it."
fi

# ── Install the Lua command channel ──────────────────────────────────────────
# This is how the broker saves and loads states: Flycast has no IPC socket and
# no default hotkeys for it, but it does embed Lua. The script is installed
# under its own name rather than flycast.lua so a script the user wrote is left
# alone; the broker selects it with -config config:LuaFileName=.
mkdir -p "${CONFIG_DIR}"
if [ -f /defaults/romm-broker.lua ]; then
  install -m 0644 -o "${PUID}" -g "${PGID}" \
    /defaults/romm-broker.lua "${CONFIG_DIR}/romm-broker.lua"
  say "Installed romm-broker.lua into ${CONFIG_DIR}."
else
  say "ERROR: /defaults/romm-broker.lua is missing; save and load states will not work."
fi

# Flycast runs as abc and has to remove the command file it consumes and write
# its own ack, so the channel cannot be root-owned.
mkdir -p "${CHANNEL_DIR}"
chown "${PUID}:${PGID}" "${CONFIG_DIR}" "${CHANNEL_DIR}" 2>/dev/null || true

# Flycast looks for dc_boot.bin and dc_flash.bin under its data directory when
# Dreamcast.BiosPath is unset. Creating it means the operator has somewhere to
# put them before the first launch rather than after it fails.
DATA_DIR="${FLYCAST_DATA_DIR:-/config/.local/share/flycast}"
mkdir -p "${DATA_DIR}"
chown "${PUID}:${PGID}" "${DATA_DIR}" 2>/dev/null || true

# ── sudo ─────────────────────────────────────────────────────────────────────
# sudo ignores a sudoers drop-in that is not exactly 0440.
if [ -f /etc/sudoers.d/romm-broker ]; then
  chmod 0440 /etc/sudoers.d/romm-broker
fi

# ── Report what the broker will find ─────────────────────────────────────────
FLYCAST_BIN="${FLYCAST_BIN:-/opt/flycast/AppRun}"
if [ ! -x "${FLYCAST_BIN}" ]; then
  say "ERROR: ${FLYCAST_BIN} is missing or not executable. Is this really a linuxserver/flycast container?"
fi
if [ -z "${BROKER_SECRET:-}${STREAMING_BROKER_SECRET:-}" ]; then
  say "WARNING: no BROKER_SECRET set. The broker runs as root and will accept every request."
fi

say "Ready. ROM_ROOT=${ROM_ROOT:-/romm/library/roms}, broker port ${BROKER_PORT:-8000}."
exit 0
