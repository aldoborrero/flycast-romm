# flycast-romm-integration

[![CI](https://github.com/aldoborrero/flycast-romm/actions/workflows/ci.yml/badge.svg)](https://github.com/aldoborrero/flycast-romm/actions/workflows/ci.yml)
[![License: GPLv3](https://img.shields.io/badge/license-GPLv3-blue.svg)](LICENSE)

A [LinuxServer Docker Mod](https://docs.linuxserver.io/general/container-customization)
that lets [RomM](https://github.com/rommapp/romm) drive
[Flycast](https://github.com/flyinghead/flycast). Pick a Dreamcast, NAOMI or
Atomiswave game in the RomM web UI and it boots in the Flycast container and
streams back to your browser.

Same shape as the [pcsx2](https://github.com/LoneAngelFayt/pcsx2-romm-integration),
[dolphin](https://github.com/LoneAngelFayt/dolphin-romm-integration) and
[xemu](https://github.com/LoneAngelFayt/xemu-romm-integration) brokers, with two
differences forced by Flycast and its container. The whole build is Nix; there
is no Dockerfile.

## Platforms

One container serves all four. They are four entries in RomM's `config.yml`
pointing at the same `broker_host`, and RomM keys its session lock on that URL,
so they correctly share one session.

| RomM slug    | System           |
| ------------ | ---------------- |
| `dc`         | Sega Dreamcast   |
| `naomi`      | Sega NAOMI       |
| `naomi2`     | Sega NAOMI 2     |
| `atomiswave` | Sammy Atomiswave |

> **Save states need a RomM change.** RomM 5.1 ships a per-platform capability
> table (`_PLATFORM_CAPABILITIES` in `backend/endpoints/streaming.py`) with no
> entry for any of these slugs, and a platform absent from it gets zero slots:
> `save-state` and `load-state` are refused with a 422 **before the broker is
> ever contacted**, and the frontend shows no slot selector. Launch, release,
> volume, mute and Save & Exit all work unpatched. The broker implements the
> full slot contract, ready for the four-line upstream addition — see
> [Contributing upstream](#contributing-upstream).

## How it works

```
browser ──── Selkies (WebRTC video) ─────────┐
                                             │
RomM backend ──── HTTP ──── broker ──── flycast
                              │
                              ├── flycast.lua   save / load state, slot, exit
                              ├── -config       transient settings per launch
                              └── pactl         volume and mute
```

The mod drops a static Go binary and a Lua script into
`lscr.io/linuxserver/flycast` and runs the binary as an s6 service on port 8000.
RomM talks to it; it talks to Flycast.

Two things differ from the sibling brokers, and both are consequences of the
container rather than choices:

**The flycast image runs Wayland, not X11.** It sets `PIXELFLUX_WAYLAND=true`,
which parks Xvfb on `sleep infinity` and runs labwc instead. `xdotool` — the
mechanism every other broker in this family uses to synthesise save and load
hotkeys — has no X server to talk to, and whether Flycast presents an Xwayland
surface or a native Wayland one is not knowable from its source.

**So control goes through Flycast's Lua API.** Flycast embeds Lua with the full
standard library and exposes `saveState`, `loadState`, `exit` and the savestate
slot directly. The mod installs a script that polls a command file each frame
and calls them. Slots are addressed by index instead of by cycling a selector,
the result comes back from inside the emulator instead of being inferred from
the filesystem, and none of it depends on which display server won.

Flycast also has **no default keyboard binding** for save or load state, so the
hotkey route would have had to write an undocumented mapping file first.

The reasoning, and the RomM contract it implements, is in
[docs/CONTRACT.md](docs/CONTRACT.md) with a file-and-line citation for every
claim.

## Quick start

```yaml
services:
  flycast:
    image: lscr.io/linuxserver/flycast:latest
    container_name: flycast
    environment:
      - PUID=1000
      - PGID=1000
      - DOCKER_MODS=ghcr.io/aldoborrero/flycast-romm-integration:latest
      - BROKER_SECRET=your_secret_here
      - ROM_ROOT=/romm/library
      - SELKIES_IS_MANUAL_RESOLUTION_MODE=true
      - SELKIES_MANUAL_WIDTH=1280
      - SELKIES_MANUAL_HEIGHT=960
    volumes:
      - ./flycast-config:/config
      - /srv/romm/library:/romm/library:ro # the same path RomM mounts
    devices:
      - /dev/dri:/dev/dri
    shm_size: 1gb
    ports:
      - "3001:3001" # Selkies stream
      - "8000:8000" # broker API
```

A fuller version, with a healthcheck and the reasoning for each setting, is in
[deploy/docker-compose.yml](deploy/docker-compose.yml). Kubernetes manifests
(single replica, `Recreate`, GPU both ways, Flux example) are in
[deploy/kubernetes/](deploy/kubernetes/).

Then point RomM at it in `config.yml`:

```yaml
streaming:
  enabled: true
  containers:
    - platform: dc
      host: https://flycast.example.com # browser-facing, must be HTTPS
      broker_host: http://flycast:8000 # server-side, optional
      label: Flycast
    - platform: naomi
      host: https://flycast.example.com
      broker_host: http://flycast:8000
      label: Flycast
    - platform: naomi2
      host: https://flycast.example.com
      broker_host: http://flycast:8000
      label: Flycast
    - platform: atomiswave
      host: https://flycast.example.com
      broker_host: http://flycast:8000
      label: Flycast
```

and set `STREAMING_BROKER_SECRET` on the RomM backend to the same value as
`BROKER_SECRET` here.

Three things are easy to get wrong:

- **`host` must be HTTPS.** RomM embeds it in an iframe, and Selkies' WebRTC
  needs a secure context. A plain-HTTP host is blocked as mixed content the
  moment RomM itself is served over TLS.
- **The ROM volume must be mounted at the same path in both containers.** RomM
  derives an absolute path from its own `LIBRARY_BASE_PATH` and assumes this
  container sees it identically. If RomM sees
  `/romm/library/dc/game.chd`, Flycast must too, or every launch 422s.
- **`broker_host` needs a scheme.** RomM rejects a bare `host:port`, silently
  skipping the container. Omit `broker_host` entirely and RomM derives it from
  `host` with the port swapped to 8000.

Confirm it came up:

```bash
docker logs flycast | grep -E 'broker|flycast-broker-mod'
curl -fsS http://localhost:8000/health
```

`"lua_ready": true` in that health response is the one to look for: without it
the emulator streams fine and save states silently do not work.

## BIOS

Flycast searches `Dreamcast.BiosPath` first and then its data directory, which
inside the container is `/config/.local/share/flycast/`. With the compose file
above that is `./flycast-config/.local/share/flycast/`.

| System          | Files                         |
| --------------- | ----------------------------- |
| Dreamcast       | `dc_boot.bin`, `dc_flash.bin` |
| NAOMI / NAOMI 2 | `naomi.zip`                   |
| Atomiswave      | `awbios.zip`                  |

The mod creates that directory on first start so the files have somewhere to go
before the first launch rather than after it fails.

## Configuration

`BROKER_SECRET` is the only one that really matters. Everything else has a
working default.

| Variable                                    | Default                         | What it does                                                                                                                                      |
| ------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `BROKER_SECRET` / `STREAMING_BROKER_SECRET` | _(none)_                        | Shared secret, sent by RomM as `X-Broker-Secret`. Unset means every request is accepted — see [Security](#security).                              |
| `BROKER_PORT`                               | `8000`                          | Port the broker listens on. RomM assumes 8000 when `broker_host` omits one.                                                                       |
| `ROM_ROOT`                                  | `/romm/library`                 | Where the library is mounted. A `rom_path` outside this is refused.                                                                               |
| `SAVE_SLOT`                                 | `10`                            | Slot `/save-and-exit` uses when RomM sends 0. Ten as the autosave slot leaves 1–9 for the player.                                                 |
| `LAUNCH_WAIT`                               | `8s`                            | How long `/launch` waits for the emulator to report running. Must stay under RomM's fixed 10s timeout.                                            |
| `SAVE_WAIT`                                 | `20s`                           | How long to wait for a savestate write to settle before giving up. Only the failure path pays it.                                                 |
| `LUA_WAIT`                                  | `10s`                           | How long to wait for the Lua script to acknowledge a command.                                                                                     |
| `QUIT_WAIT`                                 | `6s`                            | Grace between SIGTERM and SIGKILL.                                                                                                                |
| `DISPLAY_WAIT`                              | `30s`                           | How long to wait for the compositor at startup before launching anyway.                                                                           |
| `FLYCAST_BIN`                               | `/opt/flycast/AppRun`           | The emulator. `AppRun` `exec`s the real binary, so this PID is Flycast's.                                                                         |
| `FLYCAST_CONFIG_DIR`                        | `/config/.config/flycast`       | Where `emu.cfg`, the Lua script and the command channel live.                                                                                     |
| `FLYCAST_DATA_DIR`                          | `/config/.local/share/flycast`  | Where BIOS, VMU images and savestates live.                                                                                                       |
| `SSTATE_DIR`                                | _(the data dir)_                | Override only if `Dreamcast.SavestatePath` is set in `emu.cfg`.                                                                                   |
| `FLYCAST_PIDFILE`                           | `/run/flycast-romm/flycast.pid` | Records the emulator's PID, so a broker restarted after a crash stops the emulator its predecessor left running instead of starting a second one. |
| `BROKER_LOG_LEVEL`                          | `INFO`                          | `DEBUG`, `INFO`, `WARN`, `ERROR`.                                                                                                                 |
| `PUID` / `PGID`                             | _(abc's ids, else 1000)_        | Standard LinuxServer UID/GID. Also owns the files the broker creates for Flycast; unset, the mod uses the `abc` user's real ids.                  |

Durations accept both a bare number of seconds (`20`) and Go syntax (`20s`,
`1m30s`).

`RESTART_APP` must stay **off**: its watchdog relaunches the desktop's autostart
script and fights the broker for the emulator process. The mod logs a warning if
it finds it set.

## API

Everything is JSON. Send `X-Broker-Secret` on every request when
`BROKER_SECRET` is set, or get a `403`. `GET /health` is the exception, so it
works as a container healthcheck.

| Endpoint              | Does                                                                                 | Notable failures                                                                                             |
| --------------------- | ------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------ |
| `GET /health`         | Whether Flycast, the display and the Lua channel are all there                       | none                                                                                                         |
| `GET /status`         | The loaded game, whether a save is in flight, whether the crash-loop limiter gave up | none                                                                                                         |
| `POST /launch`        | Boot a ROM. Waits up to `LAUNCH_WAIT` for it to report running                       | `400` bad or out-of-root path, `422` missing or unbootable, `409` busy, `500` Flycast died during the launch |
| `DELETE /launch`      | Stop the game, back to the game list                                                 | `409` while a save or a launch is in flight                                                                  |
| `POST /save-state`    | Save to a slot without exiting. Confirms the write in the background                 | `409` no game, or a save, launch or stop in flight                                                           |
| `POST /load-state`    | Load a slot into the running game                                                    | `409` no game or the lifecycle is busy, `500` Flycast refused                                                |
| `POST /save-and-exit` | Save, confirm the write, then stop. `wait: false` to fire and forget                 | `409` no game running or another save in flight                                                              |
| `POST /volume`        | `{"level": 0-100}`                                                                   | `400` out of range, `500` pactl failed                                                                       |
| `POST /mute`          | `{"mute": true}`, or `{}` to toggle. Read back from PulseAudio                       | `500` pactl failed                                                                                           |

Slots are RomM's 1–10. Slot 10 is the autosave slot, and `0` means "use it",
which is what `/save-and-exit` sends. Internally each maps to Flycast index
`N-1`; slot 1 is the state file with no suffix.

`/save-state` answers as soon as the save is dispatched, because RomM allows it
five seconds. Poll `/status` for `save_in_progress` to know it landed.
`/save-and-exit` does block, because killing the emulator before the write
settles truncates the state.

### When Flycast dies

The broker brings the idle game list back after an unexpected exit, so the
stream never sits on a black screen — after a short pause, and only if no new
game claimed the emulator meanwhile. Three rapid deaths in a row and it gives
up rather than bury the cause under respawns: `/status` then reports
`relaunch_abandoned: true`, and the next `POST /launch` resets the limiter. A
broker that itself died uncleanly finds the emulator its predecessor left
running (through `FLYCAST_PIDFILE`) and stops it at startup instead of booting
a second one. Lifecycle calls that would interleave destructively — a stop
during a save, two launches at once — are refused with a `409` rather than
raced; the full state machine is documented in
[docs/CONTRACT.md](docs/CONTRACT.md) D8.

## Security

`BROKER_SECRET` guards port 8000. It is compared in constant time, and
`GET /health` is deliberately exempt.

**Leaving it unset is a known hole, not a supported mode.** The broker runs as
root inside the container, so an open API means root-privileged launches within
`ROM_ROOT` and root-owned writes under `/config`. It logs a warning at startup
when the secret is missing. Port 8000 is plain HTTP, so keep the broker on an
internal network or put TLS in front of it.

The Selkies desktop on 3000/3001 is a separate surface with its own auth
(`CUSTOM_USER` / `PASSWORD`), which the compose example leaves off because HTTP
basic auth prompts inside RomM's iframe. Anyone who can reach that port has the
desktop. Put it behind a proxy that authenticates, or on a network that does.

## Limitations

Most of these are the streaming framework's rather than this broker's.

- **One session at a time, per container.** RomM enforces it in Redis, keyed on
  the broker URL, and all four platforms share this one.
- **Save states live in the container**, under `/config`. They are not RomM's
  per-user saves and are not synced anywhere; back up the volume.
- **No archive extraction.** RomM hands over a path and Flycast opens it, so
  `.zip` works for NAOMI and Atomiswave carts (Flycast reads those natively) but
  a zipped Dreamcast disc image does not.
- **Multi-file games** (GDI plus tracks, CUE plus BIN) arrive as their folder,
  because that is how RomM addresses them. The broker looks inside — the folder
  itself, then one level down — and picks by format then disc number, so a
  multi-disc set boots disc 1. Any other disc has to be launched by pointing
  RomM at it directly.
- **Save states need the RomM capability entry** described at the top.
- **`lscr.io/linuxserver/flycast` is amd64 only.** The mod is published as a
  multi-arch index and will work the day an arm64 base image exists, but today
  there is nothing to attach it to.

## Performance

A GPU is not optional in practice. Software rendering through llvmpipe does not
hold 60fps on Dreamcast titles, and the Selkies encoder wants the render node
too — pass `/dev/dri` through, or use a device plugin on Kubernetes.

If the browser shows "a critical video error occurred" two or three times before
the stream finally plays, that is Selkies' VAAPI encoder path failing and
falling back to CPU JPEG. `SELKIES_USE_CPU=true` keeps H.264 without the VAAPI
path; `SELKIES_ENCODER=jpeg` skips H.264 entirely, at a real cost in bandwidth.

## Development

```bash
nix develop          # go, gopls, golangci-lint, skopeo, dive, lua, wtype
nix flake check      # go tests, the mod layout check, the devshell
nix fmt              # gofumpt, nixfmt, prettier, shfmt, stylua
nix build .#default      # the broker, static
nix build .#docker-mod   # the mod image, single layer
```

The layout is [numtide/blueprint](https://github.com/numtide/blueprint) with
`prefix = "nix/"`: each file under `nix/` maps to exactly one flake output.

The broker is stdlib-only Go. That is deliberate: it lives inside somebody
else's Debian container, a mod can only copy files, and a static binary with no
closure is the only thing that survives that trip.

`nix/checks/mod-layout.nix` is worth knowing about. The linuxserver mod loader
extracts a tarball over `/` and reports nothing, so a wrong path, a missing
executable bit or a second layer all look exactly like success. The check
asserts the shape of the image the loader will actually see.

For anything that needs a real emulator, `scripts/smoke.sh` walks the whole
contract against a live container:

```bash
STREAMING_BROKER_SECRET=... ROM=dc/game.chd scripts/smoke.sh
```

What has and has not been run on real hardware is recorded in
[docs/DEPLOY.md](docs/DEPLOY.md).

## Contributing upstream

The one change this needs from RomM is four lines in
`backend/endpoints/streaming.py`:

```python
# Flycast (dc, naomi, naomi2, atomiswave): slots 1-9 manual, slot 10 autosave.
"dc":         {"max_slots": 9, "has_autosave": True, "autosave_slot": 10},
"naomi":      {"max_slots": 9, "has_autosave": True, "autosave_slot": 10},
"naomi2":     {"max_slots": 9, "has_autosave": True, "autosave_slot": 10},
"atomiswave": {"max_slots": 9, "has_autosave": True, "autosave_slot": 10},
```

That geometry matches PCSX2 and xemu, so RomM's existing slot selector needs no
special case, and it maps cleanly onto Flycast's ten slots with none left over.
The docs page at `docs/using/emulator-streaming.md` has a table of supported
emulators that wants a row too.

## Resources

- [RomM](https://github.com/rommapp/romm) and its
  [emulator streaming docs](https://docs.romm.app/latest/using/emulator-streaming/)
- [linuxserver/flycast](https://docs.linuxserver.io/images/docker-flycast/) and
  [how Docker Mods work](https://docs.linuxserver.io/general/container-customization)
- [Flycast](https://github.com/flyinghead/flycast)
- [Selkies](https://github.com/selkies-project/selkies), which does the streaming
- Sibling brokers: [PCSX2](https://github.com/LoneAngelFayt/pcsx2-romm-integration),
  [Dolphin](https://github.com/LoneAngelFayt/dolphin-romm-integration),
  [xemu](https://github.com/LoneAngelFayt/xemu-romm-integration)

## License

[GPLv3](LICENSE), matching the sibling brokers and the wider Flycast ecosystem.
