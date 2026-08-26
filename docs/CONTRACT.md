# The RomM streaming broker contract

RomM has no formal specification for the HTTP contract between its backend and an
emulator-container broker. This document reconstructs it from source, and records
everything about the Flycast container and the Flycast emulator that a Dreamcast /
NAOMI / Atomiswave broker has to build on.

Every claim below is either **verified** (a file and line in a repository that was
read directly) or explicitly marked **UNVERIFIED**. Nothing here is inferred from
memory.

## Sources read

| What                    | Repository                                           | Revision read                    |
| ----------------------- | ---------------------------------------------------- | -------------------------------- |
| RomM backend + frontend | `rommapp/romm`                                       | `76bc157` (2026-08-23, `master`) |
| RomM docs               | `docs.romm.app/5.1.0`                                | live site, 2026-08-25            |
| PCSX2 broker            | `LoneAngelFayt/pcsx2-romm-integration`               | `main` @ clone date              |
| Dolphin broker          | `LoneAngelFayt/dolphin-romm-integration`             | `main` @ clone date              |
| xemu broker             | `LoneAngelFayt/xemu-romm-integration`                | `main` @ clone date              |
| Flycast container       | `linuxserver/docker-flycast`                         | `main` @ clone date              |
| Selkies base image      | `linuxserver/docker-baseimage-selkies`               | `main` @ clone date              |
| Debian base image       | `linuxserver/docker-baseimage-debian`                | `master` @ clone date            |
| Mod loader              | `linuxserver/docker-mods@mod-scripts/docker-mods.v3` | `3.20250825`                     |
| Flycast                 | `flyinghead/flycast`                                 | `c3763d8` (2026-08-23)           |

> Version note: the RomM documentation site is versioned `5.1.0`, while the git
> repository's newest tag is `v2.3.1`. The `master` code read here matches the
> 5.1.0 docs exactly (same platform/slot table, same config keys), so `master` is
> treated as the 5.1 line throughout.

---

## 1. Transport, auth and framing

**Verified** — `backend/endpoints/streaming.py:_broker_request`.

- Plain HTTP/1.1, JSON in and JSON out. RomM uses `urllib.request` from the
  stdlib, no keep-alive, no retries, no redirects followed by design.
- Base URL comes from `broker_host` in `config.yml`, or — when that is absent —
  from `host` with the port rewritten to **8000**
  (`_derive_broker_host`). Either value **must carry a scheme**; a bare
  `host:port` is rejected and the container is skipped with a warning.
- Auth header: **`X-Broker-Secret: <secret>`**, sent on every request only when a
  secret resolves. Resolution order (`_broker_secret`):
  1. `STREAMING_BROKER_SECRET` environment variable on the RomM backend
  2. `broker_secret` on the individual container entry in `config.yml`
     No header at all is sent when both are empty.
- `Content-Type: application/json` and an explicit `Content-Length` accompany
  every request that has a body. `DELETE /launch` is sent with no body.
- The reference brokers compare the secret with `hmac.compare_digest` and answer
  **403 `{"error": "forbidden"}`** on mismatch; `GET /health` is deliberately
  exempt so it still works as a container healthcheck
  (`dolphin .../broker.py:_check_secret`, `do_GET`).
- An empty response body is tolerated by RomM: `json.loads(raw) if raw else {}`.

## 2. Endpoints RomM actually calls

Timeouts are RomM-side and are hard: `urlopen(timeout=...)`. Exceeding one is
indistinguishable from a broker failure.

| #   | Method   | Path             | Request body                                               | RomM timeout                                                                            | Response RomM reads    | Failure handling                                                                                                                             |
| --- | -------- | ---------------- | ---------------------------------------------------------- | --------------------------------------------------------------------------------------- | ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | `POST`   | `/launch`        | `{"rom_path": "<abs path>", "rom_name": "<display name>"}` | **10 s**                                                                                | body logged only       | `HTTPError` → RomM answers **502** `Broker returned <code>: <body>`; `URLError`/`OSError` → **503**. Either way the Redis claim is released. |
| 2   | `DELETE` | `/launch`        | _(none)_                                                   | 5 s                                                                                     | ignored                | best-effort, never raises                                                                                                                    |
| 3   | `POST`   | `/save-and-exit` | `{"slot": <int 0-10>, "wait": <bool>}`                     | `STREAMING_SAVE_TIMEOUT` (**default 45 s**) when `wait=true`, **5 s** when `wait=false` | `{"saved": <bool>}`    | any error → `saved=false`, HTTP 200 to the user                                                                                              |
| 4   | `POST`   | `/volume`        | `{"level": <int 0-100>}`                                   | 5 s                                                                                     | `{"status": "ok"}`     | anything else → RomM answers **502** `Broker failed to set volume`                                                                           |
| 5   | `POST`   | `/mute`          | `{"mute": <bool>}` **or** `{}` to toggle                   | 5 s                                                                                     | `{"mute": <bool>}`     | missing/erroring → **502** `Broker failed to set mute state`                                                                                 |
| 6   | `POST`   | `/save-state`    | `{"slot": <int>}`                                          | 5 s                                                                                     | `{"status": "saving"}` | anything else → **502** `Broker failed to save state`                                                                                        |
| 7   | `POST`   | `/load-state`    | `{"slot": <int>}`                                          | **60 s**                                                                                | `{"loaded": true}`     | falsy → **502** `Broker failed to load state`                                                                                                |

Notes that matter for an implementation:

- **`/save-state` is the only route whose success value is a literal string.**
  RomM checks `body.get("status") == "saving"`, not the HTTP code. Returning
  `{"status": "ok"}` fails the check.
- **`/volume` is checked the same way**: `body.get("status") == "ok"`.
- **`/mute` returns the _confirmed_ state**, not the requested one. The reference
  brokers read it back from PulseAudio after setting it
  (`_pactl_get_mute`). RomM forwards whatever comes back to the UI.
- **`/load-state` gets a 60-second budget** because the PCSX2 broker may cycle a
  slot selector with xdotool up to nine times.
- `rom_name` is sent by RomM but **every reference broker ignores it** and derives
  the display name from the resolved filename
  (`dolphin .../broker.py` `do_POST` `/launch`; the PCSX2 README states it
  outright: _"`rom_name` is never read from the request"_).

### Endpoints RomM 5.1 does **not** call

Verified by grepping the whole backend: there is no reference to `/health`,
`/status`, `/state-file`, `/state-screenshot`, `/save-file`, `/memory-card` or
`/cleanup` anywhere outside the reference brokers' own READMEs. The
save-file/state-file sync described in the PCSX2 and Dolphin READMEs is **not**
driven by RomM 5.1 as it stands in `master`; those endpoints exist ahead of the
backend. A Flycast broker does not need them to be functional.

`GET /health` is still worth having: it is what a Docker/Kubernetes healthcheck
targets, and it is the one route the reference brokers leave unauthenticated.

## 3. Session lifecycle, as RomM sees it

**Verified** — `backend/endpoints/streaming.py`.

- Sessions live in **Redis**, not in the broker. Key:
  `romm:streaming:session:<broker-url>`. The value is JSON
  `{rom_id, rom_name, claimed_at, user_id}`.
- The key is the **derived broker URL**, so two platform entries pointing at one
  container share one session — that is how Dolphin serves `ngc`+`wii`+`wiiu`
  from a single broker, and how one Flycast container can serve `dc`, `naomi`,
  `naomi2` and `atomiswave`.
- Claiming uses `SET NX` with `ex=6h` (`SESSION_TTL_SECONDS`). A second claim
  gets **409** with `{"message": "Session in use", "rom_name", "claimed_at"}`.
- Every successful control call refreshes the TTL, so a session in use never
  expires; only an abandoned one ages out.
- `save-and-exit` with `wait=false` replaces the key with
  `{"draining": true}` at a **5-second** TTL (`SESSION_DRAIN_SECONDS`) so a
  concurrent claim cannot `/launch` on top of an emulator that is still being
  killed.
- Ownership is enforced by RomM (`user_id`, or admin). **The broker sees no user
  identity at all** and must not try to arbitrate; single-session enforcement in
  the broker is a backstop against a desynchronised Redis, not the primary lock.

### Sequence: launch → play → Save & Exit → release

```
browser                RomM backend              broker (:8000)           Flycast
   |                        |                          |                     |
   |-- POST /api/streaming/sessions {rom_id} ---------->|                     |
   |                        |                          |                     |
   |                  look up ROM row                   |                     |
   |                  rom_path = LIBRARY_BASE_PATH + rom.full_path            |
   |                  SET NX romm:streaming:session:<broker-url>              |
   |                        |                          |                     |
   |                        |-- POST /launch ---------->|                     |
   |                        |   X-Broker-Secret        kill current process   |
   |                        |   {rom_path, rom_name}    resolve rom file      |
   |                        |                          |-- spawn ----------->|
   |                        |<-- 200 {status:...} ------|   (async; the       |
   |                        |    (within 10 s)          |    reference        |
   |                        |                           |    brokers do NOT   |
   |                        |                           |    wait for a window)
   |<-- 200 {platform, host, label, rom_name, claimed_at}                     |
   |                        |                          |                     |
   |-- <iframe src="{host}"> --------------------------------- Selkies WebRTC |
   |                        |                          |                     |
   |   ... play ...         |                          |                     |
   |                        |                          |                     |
   |-- POST /sessions/{platform}/volume {level} ------->|                     |
   |                        |-- POST /volume ---------->|-- pactl ----------->|
   |                        |<-- {status:"ok"} ---------|                     |
   |                   refresh TTL                       |                    |
   |                        |                          |                     |
   |-- POST /sessions/{platform}/save-and-exit {slot,wait:true} -------------->|
   |                        |-- POST /save-and-exit --->|                     |
   |                        |   (waits up to            |  save state         |
   |                        |    STREAMING_SAVE_TIMEOUT)|  WAIT FOR THE FILE  |
   |                        |                          |  then SIGTERM ------>|
   |                        |<-- {saved: true} ---------|                     |
   |                   DELETE session key               |  relaunch idle GUI  |
   |<-- 200 {status:"ok", saved:true} -------------------                     |
   |                        |                          |                     |
   |  (navigate away instead: DELETE /sessions/{platform})                    |
   |                        |-- DELETE /launch -------->|  stop game, idle    |
```

Two details in that flow are load-bearing:

1. **The save must be on disk before the process dies.** Every reference broker
   polls the savestate directory for the write to land (new file, or changed
   mtime, then a stable size) and only then kills the emulator
   (`dolphin .../broker.py:_wait_for_sstate_write`). A fixed sleep truncates
   large states mid-flush.
2. **`/launch` returns before the game is visible.** All three reference brokers
   spawn in a background thread and answer `{"status": "launching"}` immediately.
   RomM's 10-second budget is why. See open question Q3.

## 4. How RomM delivers the ROM

**Verified** — `claim_session` in `backend/endpoints/streaming.py`.

```python
rom_path = f"{LIBRARY_BASE_PATH}/{rom.full_path}"
rom_name = rom.name or rom.fs_name_no_ext
```

- `LIBRARY_BASE_PATH` = `${ROMM_BASE_PATH}/library`, and `ROMM_BASE_PATH`
  defaults to `/romm` → **`/romm/library`** (`backend/config/__init__.py:37`).
- `rom.full_path` is `fs_path/fs_name`, and on RomM 3.0+ `fs_path` is
  `roms/<platform>` (verified against a RomM 5.2.0 library: `fs_path = roms/dc`).
  So the path the broker actually receives is
  **`/romm/library/roms/<platform>/<file>`**, and `ROM_ROOT` is that roms
  directory — not the library base.
- It is a **filesystem path, never a URL and never a download**. The comment in
  the source is explicit: _"The emulator containers mount the RomM library at the
  same path the backend uses (LIBRARY_BASE_PATH, /romm/library by default), so the
  backend-side path is valid inside the broker container too."_
  **Mounting the library at the identical path in both containers is a hard
  requirement.**
- The client never supplies a path; only `rom_id`. Visibility is re-checked
  (`assert_rom_visible`) before any broker call.
- **Multi-file ROMs arrive as a directory.** `Rom.full_path` is
  `fs_path/fs_name`, and for a multi-file ROM `fs_name` _is_ the folder. So a
  GDI+track set or a CUE+BIN set laid out one game per folder sends the broker a
  path that Flycast cannot boot directly. Every reference broker resolves this by
  looking inside: the folder itself first, then one level down, ranking candidates
  by extension preference and then by name, skipping dot-files and symlinks that
  escape `ROM_ROOT` (`dolphin .../broker.py:_resolve_rom_file`,
  `_pick_rom_file`, `_disc_number`). A folder with nothing bootable is a **422**
  carrying an `extensions` list — a distinct message from the 422 for a path that
  does not exist.
- **Loose-file multi-disc libraries send a file, not a folder.** Where each disc
  `.chd` and the `.m3u` playlist are separate ROMs (verified on RomM 5.2.0),
  launching the `.m3u` sends its path directly. Flycast cannot boot a playlist,
  so this broker resolves a `.m3u` to the first disc it names.
- No archive extraction. The RomM docs say so directly: _"The broker launches
  ROMs as direct files."_
- The reference brokers additionally confine `rom_path` under a `ROM_ROOT`
  (default `/romm/library`) and reject anything outside it with **400**
  (`_validate_rom_path`). This broker does the same, defaulting `ROM_ROOT` to
  the roms directory `/romm/library/roms`.

## 5. Save-state semantics

### Slot numbering, per platform

**Verified** — `_PLATFORM_CAPABILITIES` in `backend/endpoints/streaming.py`:

| Platform slug        | Emulator | Manual slots | Autosave slot |
| -------------------- | -------- | ------------ | ------------- |
| `ngc`, `wii`, `wiiu` | Dolphin  | 1–7          | 8             |
| `ps2`                | PCSX2    | 1–9          | 10            |
| `xbox`               | xemu     | 1–9          | 10            |

The table is the single source of truth: it gates RomM's own request validation
(`_assert_valid_slot`, a clean 422 instead of a broker 502) and it is shipped to
the frontend through `GET /api/streaming/config` so the slot selector is not a
second hard-coded copy.

> ### ⚠ Blocker: `dc` is not in that table
>
> **There is no entry for `dc`, `naomi`, `naomi2` or `atomiswave`.** A platform
> absent from `_PLATFORM_CAPABILITIES` gets `{"max_slots": 0, "has_autosave":
false, "autosave_slot": 0}`, which means, in RomM 5.1 as shipped:
>
> - `POST /streaming/sessions/dc/save-state` → **422**, before the broker is ever
>   contacted (`_assert_valid_slot`, `allow_autosave=False`).
> - `POST /streaming/sessions/dc/load-state` → **422**, same place.
> - The frontend shows **no save-slot UI** at all for the platform
>   (`platformCapabilities()` in `frontend/src/stores/streaming.ts`).
>
> What _does_ work for `dc` with an unmodified RomM 5.1: **launch, release,
> volume, mute, and save-and-exit** — the last one because `save_and_exit_session`
> never calls `_assert_valid_slot`; it passes `slot` (default **0**) straight
> through, bounded only by the pydantic `ge=0, le=10`.
>
> Getting slots for Dreamcast therefore requires a **four-line upstream PR to
> RomM** adding the platform entries. See open question Q1.

### Slot 0

**Verified** — `SaveAndExitRequest.slot` defaults to **0**, and both the PCSX2 and
Dolphin brokers treat `0` as _"use the configured default slot"_
(`# Slot 0 means "use the default slot", matching the pcsx2 broker`). The
autosave slot is that default: `SAVE_SLOT=10` for PCSX2/xemu, `SAVE_SLOT=8` for
Dolphin. So "Save & Exit" writes to the reserved autosave slot and the player's
manual slots are never clobbered.

### Save is asynchronous, exit is not

- `POST /save-state` answers `{"status": "saving"}` the moment the keystroke or
  IPC call is _dispatched_, and does the write-confirmation polling on a
  background thread. RomM's 5-second timeout makes anything else impossible.
- `POST /save-and-exit` with `wait=true` **blocks** until the state file has
  landed and the emulator has been killed, inside RomM's
  `STREAMING_SAVE_TIMEOUT` (45 s). This is the route where write confirmation is
  mandatory.
- `POST /load-state` blocks until dispatched and answers `{"loaded": bool}`.
- Concurrency guards in the reference brokers, worth copying verbatim:
  `409 {"error": "no game is running"}` when no ROM is loaded;
  `409 {"error": "save already in progress"}` for a second save, **and for a load
  during a save** — a load mid-save races the write it would overwrite.
- This broker extends those guards to the whole emulator lifecycle (see D8).
  Beyond the reference set: `DELETE /launch` is refused with
  `409 {"error": "save already in progress"}` while a save is writing — killing
  the emulator mid-write truncates the state, and the truncated file then reads
  back as a clean, stable save — and launch, stop, save and load refuse each
  other with `409 {"error": "launch in progress"}` or
  `409 {"error": "stop in progress"}` rather than interleaving kill and spawn.
  RomM's own flows never overlap these calls, so the refusals only surface for
  concurrent or retried requests.

## 6. Volume and mute

**Verified** — `dolphin .../broker.py` `do_POST`.

```
/volume  →  pactl set-sink-volume @DEFAULT_SINK@ <level>%      → {"status":"ok","level":N}
/mute    →  pactl set-sink-mute   @DEFAULT_SINK@ 1|0|toggle    → {"status":"ok","mute":<read back>}
```

- `level` is validated as an integer 0–100; out of range is **400**, a failing
  `pactl` is **500** with the stderr in `detail`.
- **`pactl` must run as `abc`, never as root.** The client library calls
  `pa_make_secure_dir()` on `PULSE_RUNTIME_PATH` and takes ownership; one root
  `pactl` chowns `/defaults` to `root:root 0700` and locks Selkies, pcmflux and
  the broker itself out of the socket for the life of the container. The xemu mod
  documents this at length in `init-xemu-audio/init.sh`.
- The sink Selkies actually streams is the null sink named **`output`**; its
  monitor `output.monitor` is what pcmflux captures. The base image creates
  `output` and `input` in `svc-selkies/run`, but only after waiting for
  PulseAudio's _pid file_, which appears before the daemon accepts connections —
  a race the xemu mod fixes by creating the sinks itself and then claiming
  `/dev/shm/audio.lock`.

Flycast also exposes an internal `AudioVolume` config (0–100) reachable from Lua;
see §9. Using PulseAudio keeps parity with the other brokers and works while the
emulator is on its idle GUI. Using both would double-attenuate.

## 7. What RomM does with `host`

**Verified** — `frontend/src/v2/views/Player/Stream.vue:547`.

```html
<iframe
  :src="containerHost"
  allow="gamepad *; fullscreen *; autoplay *"
  allowfullscreen
></iframe>
```

`host` is used **verbatim** as an iframe `src`. Consequences:

- RomM served over HTTPS + `host` over HTTP = blocked mixed content. The docs are
  blunt: _"HTTPS mandatory — Selkies WebRTC requires a secure context."_ The
  linuxserver image serves plain HTTP on **3000** and TLS on **3001**.
- `host` is what comes back in the claim response and what `GET /streaming/config`
  ships to the frontend, alongside `label` and `capabilities`.
- HTTP Basic auth on the Selkies UI (`CUSTOM_USER` / `PASSWORD`) would prompt
  inside the iframe. Leaving `PASSWORD` unset disables auth entirely
  (`docker-baseimage-selkies/README.md:43`).
- RomM never proxies the stream and never authenticates to it. The Selkies UI is
  as exposed as the network makes it — which is what pushed the PCSX2 mod to
  build its own nginx `auth_request` stream gate. That gate is a broker-family
  invention, **not** part of the RomM contract.

### `config.yml` block

```yaml
streaming:
  enabled: true
  containers:
    - platform: dc
      host: https://192.168.1.51:3001 # browser-facing, must be HTTPS
      broker_host: http://flycast:8000 # server-side, optional
      label: Flycast
      # broker_secret: per-container override for STREAMING_BROKER_SECRET
```

RomM-side environment: `STREAMING_BROKER_SECRET`, `STREAMING_SAVE_TIMEOUT`
(default **45**).

---

## 8. The container: `lscr.io/linuxserver/flycast`

**Verified** — `docker-flycast/Dockerfile`, `docker-flycast/readme-vars.yml`,
`docker-baseimage-selkies/{Dockerfile,root/**}`.

| Fact            | Value                                                                       |
| --------------- | --------------------------------------------------------------------------- |
| Base            | `ghcr.io/linuxserver/baseimage-selkies:debiantrixie` → Debian 13            |
| Flycast install | official AppImage, `--appimage-extract`ed to `/opt/flycast`                 |
| Entry binary    | **`/opt/flycast/AppRun`**                                                   |
| Architectures   | **`amd64` only** (`available_architectures` lists just `x86_64`)            |
| App user        | `abc`, `PUID`/`PGID` default 1000, in the `sudo` group with `NOPASSWD: ALL` |
| `HOME`          | `/config`                                                                   |
| Ports           | 3000 HTTP, 3001 HTTPS                                                       |
| Required        | `shm_size: 1gb` — the image's own docs call it out as needed for Flycast    |
| Notable env     | `TITLE=Flycast`, **`PIXELFLUX_WAYLAND=true`**                               |

### ⚠ The flycast image runs Wayland, not X11

This is the single biggest difference from the Dolphin/PCSX2/xemu containers and
it invalidates the "just use xdotool" assumption.

`PIXELFLUX_WAYLAND=true` is set in `docker-flycast/Dockerfile`, and in the base
image that flips three things (`docker-baseimage-selkies/root/...`):

- `svc-xorg/run` → `sleep infinity`. **There is no Xvfb.**
- `svc-de/run` → waits for the Selkies compositor socket, then runs
  `/defaults/startwm_wayland.sh`, which execs **labwc**.
- `init-selkies-config/run` → the autostart file is
  `$HOME/.config/labwc/autostart` (copied from `/defaults/autostart_wayland`
  **only if it does not already exist**), and `XDG_RUNTIME_DIR` is `/config/.XDG`.

The nesting is: Selkies/pixelflux is the outer compositor on `wayland-1`; labwc
is its client and provides `wayland-0` to applications; labwc is built
`-Dxwayland=enabled`, and `svc-watchdog/run` shows the app environment as
`WAYLAND_DISPLAY=wayland-0`, `DISPLAY=:0`.

So an Xwayland display at `:0` does exist, but whether Flycast is an X11 client on
it or a native Wayland client depends on what SDL2 picks at runtime.
**UNVERIFIED — needs a live container to settle** (see Q2).

What the base image gives us to work with, all verified in its `Dockerfile`:

| Tool               | Present | Path / note                                                                                                                                                                 |
| ------------------ | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `wtype`            | ✅      | `/usr/bin/wtype`, built from source in a dedicated stage. Wayland virtual-keyboard injection — goes to the compositor's focused surface, which covers Xwayland clients too. |
| `xdotool`          | ✅      | package installed, but useless if Flycast is a native Wayland client                                                                                                        |
| `xdpyinfo`, `xset` | ✅      | via `x11-utils`                                                                                                                                                             |
| `pactl`            | ✅      | via `pulseaudio-utils`                                                                                                                                                      |
| `xwayland`         | ✅      | package installed; labwc built with it enabled                                                                                                                              |
| `sudo`             | ✅      | `abc` has `NOPASSWD: ALL`; can be revoked by `DISABLE_SUDO`/`HARDEN_DESKTOP`                                                                                                |
| `python3`          | ❔      | **UNVERIFIED.** The Dolphin mod apt-installs it at init, implying it is not guaranteed. Irrelevant for a Go broker.                                                         |

### labwc has an IPC socket

The base image applies its own `labwc-ipc.patch` (labwc 0.9.7). It adds a Unix
socket at **`$XDG_RUNTIME_DIR/labwc.sock`** — i.e. `/config/.XDG/labwc.sock` —
speaking one line-delimited command per connection:

- `GET_WINDOWS` → JSON array of `{pid, x, y, width, height, title, app_id,
minimized, fullscreen, maximized, tiled, focused}`
- `GET_WINDOW_BY_PID <pid>` → one such object, or `null`
- `GET_FOCUSED_WINDOW` → same, or `null`

Read-only introspection, no input injection. It is **exactly** the "wait for the
window" primitive, and far more reliable than polling `_NET_CLIENT_LIST`.

**Catch:** the socket only exists when labwc is started with `-i`, and
`startwm_wayland.sh` passes `-i` only on the `PELORUS=true` branch. Enabling it
means the mod patches `/defaults/startwm_wayland.sh` before `svc-de` starts —
the same class of surgery the PCSX2 mod does to nginx, with the same ordering
requirement.

### Selkies environment worth pinning

From `docker-baseimage-selkies/README.md` and `init-selkies-config/run`:

| Variable                                         | Effect                                                                                                                                           |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `SELKIES_MANUAL_WIDTH` / `SELKIES_MANUAL_HEIGHT` | lock the resolution; setting either activates manual mode                                                                                        |
| `SELKIES_IS_MANUAL_RESOLUTION_MODE`              | manual mode at the default 1024x768                                                                                                              |
| `PASSWORD` / `CUSTOM_USER`                       | HTTP basic auth; **unset `PASSWORD` = no auth**, which is what an iframe embed wants                                                             |
| `SELKIES_ENCODER`, `SELKIES_USE_CPU`             | `x264enc,jpeg` default; the PCSX2 README documents a VAAPI decoder crash loop that `SELKIES_USE_CPU=true` works around                           |
| `NO_DECOR`, `NO_FULL`                            | window decorations / auto-maximise                                                                                                               |
| `RESTART_APP`                                    | watchdog relaunches the autostart app and locks the autostart file `root:abc 0550` — **conflicts with a broker that owns the process lifecycle** |
| `HARDEN_DESKTOP` / `HARDEN_OPENBOX`              | bundles; `HARDEN_OPENBOX` implies `RESTART_APP=true`, and `HARDEN_DESKTOP` implies `DISABLE_SUDO=true`                                           |
| `DRINODE`                                        | pick a specific render node                                                                                                                      |

### How `DOCKER_MODS` applies a mod — and its hard constraint

**Verified** — `docker-mods.v3` (`3.20250825`), pinned by the Debian base image
as `S6_STAGE2_HOOK=/docker-mods`.

1. `DOCKER_MODS` is a `|`-separated list. A bare `owner/repo` resolves against
   `lscr.io`; a `ghcr.io/...` reference is used as given.
2. It fetches the manifest and takes **`.layers[0].digest`** — for a multi-arch
   index, after selecting `.manifests[] | select(.platform.architecture == ARCH)`.
3. It downloads that **one blob**, verifies it with `tar -tzf` (so: **gzip**),
   extracts to `/tmp/mod`, deletes any v2 `cont-init.d` / `services.d`
   directories, renumbers `notification-fd` files, then `cp -R /tmp/mod/* /`.
4. It records the layer digest in `/<mod-name>` so a re-run is a no-op.

> **Constraint: the mod image must be single-layer.** Only `layers[0]` is ever
> fetched; anything in a second layer silently does not exist. In Nix terms this
> rules out `dockerTools.buildLayeredImage` / `streamLayeredImage` and points at
> `dockerTools.buildImage` with no `fromImage`, which produces exactly one layer.
> Multi-arch _is_ supported, via a proper OCI index.

s6-overlay **v3** only: `cont-init.d` and `services.d` from v2 are actively
deleted. The mod ships `/etc/s6-overlay/s6-rc.d/<name>/{type,run,up,finish,
dependencies.d/*}` plus a stanza in `user/contents.d/`.

A mod can also install distro packages by shipping
`/mod-repo-packages-to-install.list` (one package per line); `init-mods-package-install`
runs `apt-get install -y --no-install-recommends` over it after all mods are
applied (`package-install.v1`).

### Reference mod layout, for symmetry

```
root/etc/s6-overlay/s6-rc.d/
  init-<emu>-config/{type=oneshot, up→init.sh, init.sh, dependencies.d/init-config-end}
  init-<emu>-deps/ {type=oneshot, ...}                 # PCSX2: slow/networked work only
  svc-broker/      {type=longrun, run, finish, dependencies.d/{init-<emu>-config,legacy-cont-init}}
  init-services/dependencies.d/init-<emu>-config       # forces the service stack to wait
  user/contents.d/{init-<emu>-config, svc-broker}
root/etc/sudoers.d/broker                              # `root ALL=(abc) NOPASSWD: /usr/bin/env`
root/root/broker.py
```

Two ordering lessons the PCSX2 README states explicitly and that carry over:

- Anything that patches a file a base service reads **once at startup** must run
  before that service, which means adding an edge into
  `init-services/dependencies.d/`. Patch it later and the file on disk changes
  while the running process does not — a silent failure.
- Slow or networked init (apt) belongs in a _separate_ oneshot that only
  `svc-broker` depends on, so a registry timeout cannot stall the stream.

The reference mods are published from a two-line `Dockerfile` (`FROM scratch;
COPY root/ /`), pushed with `docker/build-push-action` and
`outputs: type=registry,compression=gzip,force-compression=true` — the gzip flag
being exactly what `tar -tzf` in the mod loader requires.

---

## 9. Flycast itself

**Verified** — `flyinghead/flycast` @ `c3763d8`.

### Command line

`core/cfg/cl.cpp`:

```
flycast [-config section:key=value,...] [<rom path>]
```

- `-config` sets **transient** values, not written back to `emu.cfg`. Multiple
  `key=value` pairs are comma-separated, and values may be quoted.
- Extension handling for the positional argument: `.cdi`, `.chd`, `.gdi`, `.cue`
  are logged as a CD image; `.elf` additionally forces `bios.UseReios=yes`;
  **anything else is accepted as a "rom"** — which is how NAOMI/Atomiswave `.zip`
  and `.7z` sets load.
- There is no `--fullscreen`, no `--save-state`, no `--batch`. **Do not invent
  flags**: the only two options the parser knows are `-config` and `-help`;
  everything else is warned about and ignored.

### `AppRun` is transparent

`shell/linux/make-appimage.sh` generates `AppRun` as a three-line `/bin/sh`
script that ends in `exec "$APPDIR/usr/bin/flycast" "$@"`. Because it `exec`s,
the shell process is _replaced_: the PID the broker spawns **is** the Flycast
process, so ordinary `Popen` + `wait()` supervision works with no wrapper-PID
indirection, and `-config ...` arguments pass straight through. The script may
prepend to `LD_LIBRARY_PATH` and `LD_PRELOAD` via `checkrt`, preserving anything
already set — so an `LD_PRELOAD` the broker sets for the Selkies joystick
interposer survives.

### Paths

`core/linux-dist/main.cpp` — with `HOME=/config` inside the container:

| What             | Path                                                                           |
| ---------------- | ------------------------------------------------------------------------------ |
| Config dir       | `$XDG_CONFIG_HOME` or `$HOME/.config` → **`/config/.config/flycast/`**         |
| Data dir         | `$XDG_DATA_HOME` or `$HOME/.local/share` → **`/config/.local/share/flycast/`** |
| Main config file | `emu.cfg` in the config dir                                                    |
| Input mappings   | `mappings/<name>.cfg` under the config dir                                     |
| Lua script       | `get_readonly_config_path(LuaFileName)`, default `flycast.lua`                 |
| Savestates       | `get_writable_data_path(...)` unless `Dreamcast.SavestatePath` is set          |
| BIOS             | searched under `Dreamcast.BiosPath`, then data dirs (`hostfs::findFlash`)      |

`emu.cfg` uses a `[config]` section with dotted keys — `Option`'s default section
is literally `"config"` (`core/cfg/option.h:107`), so `Dreamcast.SavestateSlot`
lives at `[config] Dreamcast.SavestateSlot`. On the command line that is
`-config config:Dreamcast.SavestateSlot=2`.

Relevant option keys, all verified in `core/cfg/option.cpp`:

| Key                       | Meaning                                                                          |
| ------------------------- | -------------------------------------------------------------------------------- |
| `Dreamcast.SavestateSlot` | current slot, **0–9** (`(slot + 10 + step) % 10`), shown in the UI as "Slot N+1" |
| `Dreamcast.AutoSaveState` | save to the current slot when the game is unloaded                               |
| `Dreamcast.AutoLoadState` | load the current slot when a game starts                                         |
| `Dreamcast.SavestatePath` | list of directories to write savestates to                                       |
| `Dreamcast.BiosPath`      | list of directories to search for BIOS                                           |
| `Dreamcast.ContentPath`   | list of game directories for the built-in game list                              |
| `LuaFileName`             | script filename, default `flycast.lua`                                           |

### Savestate filenames

`core/oslib/oslib.cpp:getSavestatePath`:

```
<rom basename><suffix>.state       suffix = "" for index 0, "_<index>" for index 1..99
```

Index `-1` appends `.net` (GGPO) and `-2` appends `.tmp` (in-RAM states). The
savestate carries an embedded 640px PNG screenshot when saved through the GUI
path (`core/ui/gui.cpp:savestate()`).

### ⚠ Flycast ships **no** default keyboard hotkeys for save/load state

`core/input/keyboard_device.h:KeyboardInputMapping` binds only:

```
TAB → menu       Space → fast-forward       F12 → screenshot
X C S D / arrows / Return / F / V / I K J L / Q  → Dreamcast pad
```

`EMU_BTN_SAVESTATE`, `EMU_BTN_LOADSTATE`, `EMU_BTN_NEXTSLOT`, `EMU_BTN_PREVSLOT`
and `EMU_BTN_ESCAPE` exist as bindable actions (`core/ui/settings_controls.cpp`,
dispatched in `core/input/gamepad_device.cpp:100-140`) but **have no default key**.
A hotkey-based broker would first have to write a `mappings/*.cfg` that binds
them — a file format that is Flycast's, undocumented, and version-sensitive.

### ✅ Flycast has a Lua scripting API — this is the robust control channel

`core/lua/lua.cpp`, gated by `USE_LUA` which **defaults to `ON`**
(`CMakeLists.txt:102`) and is genuinely enabled in the official Linux build: CI
installs `liblua5.3-dev` (`.github/workflows/c-cpp.yml:52`) and
`shell/linux/make-appimage.sh` bundles **`liblua5.3.so.0`** into the AppImage
that the linuxserver image extracts.

The script is `luaL_dofile`'d at startup after `luaL_openlibs(L)` — so the full
Lua standard library, **`io` and `os` included**, is available.

Exposed API:

```lua
flycast.emulator.startGame(path)        flycast.emulator.stopGame()
flycast.emulator.pause()                flycast.emulator.resume()
flycast.emulator.saveState(index)       flycast.emulator.loadState(index)
flycast.emulator.exit()                 flycast.emulator.displayNotification(msg)

flycast.config.general.SavestateSlot   -- int, read/write
flycast.config.general.AutoSaveState   -- bool
flycast.config.general.AutoLoadState   -- bool
flycast.config.audio.AudioVolume       -- int
-- plus most video/audio/advanced options as properties
```

Callbacks are functions on a global table named `flycast_callbacks`; the ones
that matter are **`overlay`** (called every frame while the overlay draws) and
**`vblank`** (`Event::VBlank`), with `start`, `pause`, `resume`, `terminate`,
`loadState`, `diskChange` also available.

That is a complete, in-process command channel: the mod installs a `flycast.lua`
that polls a command file or FIFO on each frame and calls `saveState(n)` /
`loadState(n)` / `exit()` directly, writing an acknowledgement back for the
broker to read. It sidesteps the whole Wayland-vs-X11 input-injection question,
it addresses slots by number instead of cycling a selector, and it is the only
mechanism here that can report _"the save actually happened"_ from inside the
emulator rather than by watching the filesystem.

`AutoSaveState` + `AutoLoadState` are a second, cruder lever: with both on,
Flycast saves to the current slot on unload and reloads it on start, which is
"Save & Exit" and "resume" for free — at the cost of overwriting whatever slot is
current on _every_ game exit.

### Idle behaviour

Flycast with no ROM argument opens its own game-list GUI, which is a perfectly
good analogue of the "dashboard" the other brokers keep alive so the stream is
never a black screen. `Dreamcast.ContentPath` populates that list.

### BIOS

`hostfs::findFlash("dc_", "%bios.bin;%boot.bin")` searches `Dreamcast.BiosPath`
first, then the data directories. Dreamcast wants `dc_boot.bin` + `dc_flash.bin`
(the game scanner probes for `dc_bios.bin`/`dc_boot.bin`); NAOMI wants
`naomi.zip`, Atomiswave `awbios.zip`
(`core/hw/naomi/naomi_cart.cpp`, `core/hw/naomi/naomi_roms.cpp`). Exact required
filename set per platform is **UNVERIFIED** beyond those references.

---

## 10. Decisions

The questions this document raised were put to the maintainer and answered on
2026-08-25. What follows is the resolution for each, and the reasoning where the
call was delegated back to me.

### D1 — Save-state slots: implement the full contract (was Q1)

The broker implements RomM's complete slot contract. The upstream PR adding
`dc`, `naomi`, `naomi2` and `atomiswave` to `_PLATFORM_CAPABILITIES` is the
maintainer's to open; this repository is written as though it has landed, and the
README states the RomM version requirement.

Geometry, matching PCSX2/xemu so RomM's existing UI needs no special case:

| RomM slot               | Meaning                            | Flycast `SavestateSlot` index | State file                                                                      |
| ----------------------- | ---------------------------------- | ----------------------------- | ------------------------------------------------------------------------------- |
| 1–9                     | manual                             | 0–8                           | `<basename>_1.state` … `<basename>_8.state`, and `<basename>.state` for index 0 |
| 10                      | autosave, reserved for Save & Exit | 9                             | `<basename>_9.state`                                                            |
| 0 (on `/save-and-exit`) | alias for the autosave slot        | 9                             | `<basename>_9.state`                                                            |

So `flycast_index = romm_slot - 1`, and Flycast's ten slots (`(n + 10 + step) %
10`, verified in `core/ui/gui.cpp:520`) map exactly onto RomM's 1–10 with none
left over. Note the off-by-one trap in the filenames: `getSavestatePath` writes no
suffix at all for index 0, so RomM slot 1 is `<basename>.state`, not
`<basename>_0.state`.

The proposed upstream entry:

```python
# Flycast (dc, naomi, naomi2, atomiswave): slots 1-9 manual, slot 10 autosave.
"dc":         {"max_slots": 9, "has_autosave": True, "autosave_slot": 10},
"naomi":      {"max_slots": 9, "has_autosave": True, "autosave_slot": 10},
"naomi2":     {"max_slots": 9, "has_autosave": True, "autosave_slot": 10},
"atomiswave": {"max_slots": 9, "has_autosave": True, "autosave_slot": 10},
```

### D2 — Emulator control goes through Flycast's Lua API (was Q2 and Q4)

Delegated to me. **The broker controls Flycast through a Lua command channel, not
through synthetic keystrokes, and the mod patches no base-image file.**

Why not keystrokes. Flycast ships no default binding for save state, load state
or slot cycling (`core/input/keyboard_device.h`), so a hotkey broker must first
write a `mappings/*.cfg` in a format that is Flycast's, undocumented and free to
change between releases. It would then have to guess whether `xdotool` (Xwayland)
or `wtype` (Wayland virtual-keyboard) reaches the window, a question that cannot
be answered from source and that changes with whatever SDL2 decides at runtime.
And a keystroke is fire-and-forget: success can only ever be inferred by watching
the filesystem.

Why Lua. `USE_LUA` defaults `ON`, CI builds against `liblua5.3-dev`, and
`make-appimage.sh` bundles `liblua5.3.so.0` into the AppImage the container
extracts — so the interpreter is present in the shipped binary. `lua::init()`
runs once in `flycast_init()` (`core/nullDC.cpp:110`), **after**
`config::parseCommandLine` at line 82, so `-config config:LuaFileName=...` picks
our script instead of clobbering a user's `flycast.lua`. `luaL_openlibs` gives the
script `io` and `os`. The API addresses slots by index, so there is no selector to
cycle, and `saveState` returns only once `dc_savestate` has written the file — the
first mechanism in this broker family that can report a save from inside the
emulator rather than by inferring it from stat() calls.

Protocol, deliberately minimal, in `${FLYCAST_CONFIG_DIR}/romm-broker/`:

| File      | Written by                              | Contents                                                    |
| --------- | --------------------------------------- | ----------------------------------------------------------- |
| `ready`   | Lua, at script load                     | one line, the protocol version — proves the script loaded   |
| `command` | broker, atomically (`write` + `rename`) | `<seq> <verb> [arg]` — `save N`, `load N`, `slot N`, `exit` |
| `ack`     | Lua, after executing                    | `<seq> ok` or `<seq> err <reason>`                          |
| `state`   | Lua, on `start` / `terminate`           | `running` / `stopped`                                       |

The poll runs in the **`overlay`** callback, not `vblank`. `lua::overlay()` is
invoked from `gui_draw_osd()` on the render/UI thread
(`core/ui/gui.cpp:1479`, called from `core/rend/gles/gles.cpp:1058` and
`core/rend/vulkan/vulkan_context.cpp:1174`), which is the thread
`flycast.emulator.saveState` is shaped for — it calls `gui_open_settings()`,
which calls `emu.stop()`, and driving that from the emulation thread on a `vblank`
callback risks deadlocking against itself. `vblank` is left unused. The cost is
that `overlay` only fires while a game is rendering, which is exactly and only
when save/load are meaningful. **UNVERIFIED: the thread-safety reasoning is from
reading the source, not from a running emulator** — the smoke test settles it.

Two mechanisms stay as backstops, and are documented rather than wired in:

- **Filesystem confirmation** is kept even though Lua acks the save. The broker
  still polls the state file for a new-or-changed mtime and then a stable size
  before it lets `/save-and-exit` kill the process, exactly as the Dolphin broker
  does. This is mandatory, not defensive: `dc_savestate` opens the final path
  with `"wb"` and writes in place (`core/nullDC.cpp` — the tmp variant at index
  -2 is an in-RAM quicksave, not a temp file), so an interrupted write is a
  truncated state, and a Lua ack that arrives before the OS has flushed proves
  nothing about the bytes.
- **`wtype`** is present in the base image and is the fallback if Lua turns out to
  be unavailable at runtime. The broker detects that case — no `ready` file
  within the launch window — and reports it on `/health` instead of failing
  silently. Whether the fallback is implemented at all is deferred until the
  smoke test says whether it is ever needed.

No base-image file is patched (this is the Q4 answer): the labwc IPC socket is
**not** used, so `/defaults/startwm_wayland.sh` is left alone. Window readiness
comes from the Lua `ready`/`state` files, which report that the emulator is
actually running rather than that a surface exists — strictly better information,
obtained without a `sed` that a linuxserver release can silently break.

The mod does still write `$HOME/.config/labwc/autostart` to stop the desktop from
launching its own Flycast, the way the Dolphin mod does. That is a file in the
`/config` volume, not base-image content, and the oneshot that writes it takes an
`init-services` dependency edge so it lands before `svc-de` reads it.
`RESTART_APP` must stay off; its watchdog would fight the broker for the process
and lock the autostart file to `root:abc 0550`.

### D3 — `/launch` waits, but never past RomM's timeout

Not contested, so my recommendation stands. `POST /launch` blocks until the Lua
`state` file reports `running`, with a hard cap (`LAUNCH_WAIT`, default **8 s**)
chosen to sit inside RomM's fixed 10-second budget. On timeout it answers `200`
anyway with `"ready": false` rather than failing: the emulator is still coming up,
and a 502 would release RomM's claim out from under a boot that is going to
succeed.

### D4 — Volume through PulseAudio

Not contested. `pactl set-sink-volume @DEFAULT_SINK@ N%` as `abc`, matching the
sibling brokers, working while the emulator sits on its game list, and leaving
Flycast's own `AudioVolume` untouched so the two cannot double-attenuate.

### D5 — Both systems in the flake (was Q6)

Confirmed: `systems = [ "x86_64-linux" "aarch64-linux" ]`, and the mod is
published as a proper multi-arch OCI index. `docker-mods.v3` selects by
`.platform.architecture`, so the index costs nothing and is correct the day
linuxserver publishes an arm64 flycast. The README states plainly that
`lscr.io/linuxserver/flycast` is amd64-only today.

The mod image is built with `dockerTools.buildImage` — **single layer,
mandatory**, because the mod loader only ever fetches `layers[0]`.

### D6 — One container, four platforms

Not contested. `dc`, `naomi`, `naomi2` and `atomiswave` are four `containers:`
entries in `config.yml` all pointing at one `broker_host`. RomM keys its session
lock on the derived broker URL, so the four correctly share a single session —
`backend/tests/endpoints/test_streaming.py::test_claim_session_same_container_two_platforms_rejected`
covers exactly this shape. The compose and Kubernetes examples are written around
one container.

### D7 — What cannot be tested in this workspace

`nix` is available (Determinate Nix 3.22.2, at
`/nix/var/nix/profiles/default/bin/nix`) and Docker is installed, but no live
Flycast container has been exercised here. Everything in `docs/DEPLOY.md` that
requires a running emulator is recorded as **not executed**, with the exact
commands to run it on a real host.

### D8 — The lifecycle is one state machine: launch, stop and save exclude each other

Added after a review of the first implementation found three destructive
interleavings between the lifecycle calls. The session manager holds one claim
per operation, and every path that kills, spawns or writes takes one first:

- **A stop claims the session like a launch does.** A `DELETE /launch` racing
  an in-flight `/save-state` used to SIGTERM the emulator mid-write; the
  truncated file then stopped growing, and the write confirmation read it back
  as success. It is now refused with the same 409 every other save conflict
  gets. The clean-shutdown path already waited for in-flight saves; this closes
  the API path that did not.
- **Save claims are numbered.** The claim of a save abandoned to a crash is
  released when the exit monitor clears the session, and the abandoned
  goroutine's own deferred release — which can fire up to `SAVE_WAIT` later —
  presents a stale token and becomes a no-op instead of freeing the claim a
  newer save holds by then. `/save-and-exit` re-checks its token between the
  save and the kill, so after a mid-save crash it leaves whatever newer session
  owns the emulator alone instead of killing it.
- **The idle relaunch is gated.** The return to the game list after a stop, a
  save-and-exit or a crash runs in the background, and re-checks under the
  launch lock that the session is still idle — so a menu never replaces a game
  that launched while the relaunch was queued.
- **A session record cannot outlive its process.** A game that boots and dies
  inside the launch window is invisible to the monitor's relaunch (it defers
  to the launch's claim), so `/launch` checks the process survived before
  recording the session — and answers 500, which makes RomM release its claim
  on a ROM that crashes the emulator. As a second layer, `/status` and
  `/health` derive `active` from the live process at read time, the way the
  pcsx2 broker computes it, instead of trusting the stored record.
- **The crash relaunch is a limited loop, not a single strike.** An unexpected
  exit relaunches the game list after a short pause (5 s when the process died
  within five seconds of launching, 1 s otherwise), and three consecutive
  rapid deaths make the monitor give up — the same counter, pacing and limit
  as the pcsx2 broker. Giving up is visible: `/status` reports
  `relaunch_abandoned: true` (while `/health` stays an unconditional 200 for
  container healthchecks, as in every sibling broker), and an explicit
  `POST /launch` resets the limiter and recovers.
- **A leftover emulator is reaped at startup.** Flycast is spawned with
  `setsid` and survives an unclean broker death, and s6 restarts only the
  broker — which would then boot a second emulator on top of the orphan. The
  spawned group leader's PID is recorded in `FLYCAST_PIDFILE` (default
  `/run/flycast-romm/flycast.pid`, on tmpfs so a fresh container never
  inherits it); at startup the broker terminates a live leftover found there,
  but only after `/proc/<pid>/cmdline` matches `FLYCAST_BIN`, so a recycled
  PID belonging to an innocent process is left alone.

## 11. Known unknowns (not blocking, but unverified)

- The exact BIOS filename set Flycast requires per platform, beyond
  `dc_boot.bin` / `dc_flash.bin` / `naomi.zip` / `awbios.zip`.
- Whether the Selkies joystick interposer (`LD_PRELOAD` of
  `selkies_joystick_interposer.so` + the fake libudev) is needed for Flycast, or
  whether it is applied by the base image already. The Dolphin mod sets it
  explicitly in the child environment.
- Flycast's behaviour when handed a `.chd` for a NAOMI cart vs a Dreamcast disc —
  the CLI treats `.chd` as a CD image unconditionally.
- Whether calling `flycast.emulator.saveState` from the Lua `overlay` callback is
  safe in practice. The reasoning in D2 says yes (it is the render/UI thread, the
  one `gui_open_settings` is shaped for) but only a running emulator proves it.
- Whether the `overlay` callback fires often enough on a heavily loaded frame to
  keep command latency acceptable. It is once per rendered frame, so it should
  track the framerate, but a stalled renderer stalls the command channel too.
