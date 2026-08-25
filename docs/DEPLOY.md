# Deploying, and what has actually been tested

Two things live here: how to get this running, and an honest record of which
checks have been executed and which have not.

## Test matrix

The workspace this was built in has Nix and Docker but no GPU, no display, and
no way to run `lscr.io/linuxserver/flycast` end to end. So the split below is
real and worth reading before trusting anything in the "not executed" half.

### Executed

| #   | Check                                             | How                                                 | Result                                                                                                                                                 |
| --- | ------------------------------------------------- | --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | The flake evaluates and every check passes        | `nix flake check`                                   | pass — `go-test`, `mod-layout`, `pkgs-docker-mod`, `devshell-default`                                                                                  |
| 2   | The tree is formatted                             | `nix fmt -- --fail-on-change`                       | pass                                                                                                                                                   |
| 3   | Unit tests                                        | `go test ./...`                                     | pass — session guards, ROM resolution, savestate naming, write settling, the Lua protocol against a fake script, every route against a fake controller |
| 4   | The broker is a static binary                     | `file result/bin/romm-broker`                       | pass — `ELF 64-bit LSB executable, statically linked, stripped`, 6.7 MB                                                                                |
| 5   | The mod image is single-layer                     | `jq '.[0].Layers \| length' manifest.json`          | pass — `1`                                                                                                                                             |
| 6   | The layer contains only mod files                 | `tar -tvf layer.tar`                                | pass — 16 files, 2.8 MB, no `/nix` tree                                                                                                                |
| 7   | s6 file names, types and modes                    | `nix build .#checks.x86_64-linux.mod-layout`        | pass                                                                                                                                                   |
| 8   | The image is labelled with the right architecture | same check                                          | pass — `amd64`                                                                                                                                         |
| 9   | The Lua script parses                             | `lua -e 'loadfile(...)'` under Lua 5.4              | pass                                                                                                                                                   |
| 10  | The shell scripts parse                           | `bash -n` on `init.sh`, `run`, `finish`, `smoke.sh` | pass                                                                                                                                                   |

### Not executed — needs a real container

Every row here is a claim this repository makes that only a live Flycast
container can settle. `scripts/smoke.sh` covers rows 11–22 in one run.

| #   | Check                                                             | Why it matters                                                                                                                                                          | How to run it                                    |
| --- | ----------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| 11  | The mod applies at all                                            | The loader reports nothing on failure                                                                                                                                   | `docker logs flycast \| grep flycast-broker-mod` |
| 12  | The broker starts and answers `/health`                           | Everything else depends on it                                                                                                                                           | `curl -fsS localhost:8000/health`                |
| 13  | **`lua_ready` is true**                                           | The single biggest unverified assumption. If Flycast's AppImage turns out not to have Lua, or the script does not load, streaming works and save states silently do not | the same `/health`                               |
| 14  | Flycast boots a Dreamcast `.chd` and appears in the stream        | —                                                                                                                                                                       | `POST /launch`                                   |
| 15  | A folder-organised GDI set resolves to its disc image             | RomM addresses multi-file ROMs by folder                                                                                                                                | `POST /launch` with the folder                   |
| 16  | `POST /save-state` writes `<basename>_N.state`                    | The slot mapping is off-by-one by construction                                                                                                                          | `ls /config/.local/share/flycast/`               |
| 17  | `POST /load-state` restores it                                    | —                                                                                                                                                                       | `POST /load-state`                               |
| 18  | `/save-and-exit` returns `saved: true` and the state is complete  | The whole point of the write-settling wait                                                                                                                              | check the file size after                        |
| 19  | **`saveState` from the Lua `overlay` callback does not deadlock** | Reasoned from the source in `docs/CONTRACT.md` D2, not observed. It runs on the render thread, which is the one `gui_open_settings` is shaped for                       | rows 16–18 passing is the proof                  |
| 20  | `pactl` reaches abc's PulseAudio and volume audibly changes       | Running `pactl` as root once poisons `/defaults` for the whole container                                                                                                | `POST /volume` then listen                       |
| 21  | An unexpected emulator exit brings the game list back             | —                                                                                                                                                                       | `docker exec flycast pkill flycast`              |
| 22  | A NAOMI `.zip` and an Atomiswave `.zip` boot                      | Needs `naomi.zip` / `awbios.zip` in place                                                                                                                               | `POST /launch`                                   |
| 23  | RomM's play button appears and the iframe streams                 | The end-to-end goal                                                                                                                                                     | the RomM UI                                      |
| 24  | Save slots appear in RomM's UI                                    | **Blocked** on the upstream capability entry; see the README                                                                                                            | the RomM UI                                      |
| 25  | The Kubernetes manifests apply and the pod becomes ready          | Written from the docs, never applied                                                                                                                                    | `kubectl apply -k deploy/kubernetes`             |

Fill in results as you run them, rather than deleting rows: a row that has been
run and failed is more useful than one that is missing.

## Docker Compose

[`deploy/docker-compose.yml`](../deploy/docker-compose.yml) is the reference. It
needs three things from you:

```bash
export STREAMING_BROKER_SECRET="$(openssl rand -hex 32)"
```

- the same secret on RomM's backend
- the library bind mount pointed at your real library, **at the same path RomM
  uses inside its own container**
- BIOS in `./flycast-config/.local/share/flycast/`

```bash
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml logs -f flycast
```

The startup sequence to look for:

```
[flycast-broker-mod] Disabled the labwc autostart.
[flycast-broker-mod] Installed romm-broker.lua into /config/.config/flycast.
[flycast-broker-mod] Ready. ROM_ROOT=/romm/library, broker port 8000.
... level=INFO msg="flycast romm broker starting" version=...
... level=INFO msg="shared secret auth enabled"
... level=INFO msg="display is up" display=wayland-0
... level=INFO msg="launching flycast" rom="game list"
... level=INFO msg="broker listening" port=8000 rom_root=/romm/library
```

If `lua_ready` is false in `/health`, check in this order:

1. `docker exec flycast ls -la /config/.config/flycast/` — is `romm-broker.lua`
   there, owned by `abc`?
2. `docker exec flycast ls -la /config/.config/flycast/romm-broker/` — is the
   channel directory there and writable by `abc`? A `ready` file should exist
   once Flycast has started.
3. `docker logs flycast | grep -i lua` — Flycast logs `Lua error:` if the script
   throws, and nothing at all if it was never found.
4. If the script is present and correct and `ready` never appears, the AppImage
   was probably built without Lua. That would invalidate the whole control
   design; please open an issue with `docker exec flycast ls /opt/flycast/usr/lib
| grep lua`.

## TLS

RomM embeds the stream in an iframe, and Selkies needs a secure context, so
`host` in RomM's `config.yml` must be HTTPS. Two shapes work:

**Direct to 3001.** The image serves TLS there with a self-signed certificate.
The browser will refuse it inside an iframe until the user has accepted it once
by visiting the URL directly. Workable for a home lab, unpleasant for anyone
else.

**Behind a reverse proxy** that terminates a real certificate and forwards to
3001 with upstream verification off. This is what
[`deploy/kubernetes/ingress.yaml`](../deploy/kubernetes/ingress.yaml) does. Give
the proxy long read and send timeouts — WebRTC signalling is a long-lived
WebSocket.

## Kubernetes

```bash
kubectl apply -k deploy/kubernetes
```

Before that, override at least: the ingress host and issuer, the
`DOCKER_MODS` tag (pin it), the `romm-library` PVC name, the storage class, and
the secret. [`kustomization.yaml`](../deploy/kubernetes/kustomization.yaml)
lists them.

Two structural constraints, both deliberate:

**One replica, `Recreate`.** RomM holds a single session per broker URL, the
config volume is ReadWriteOnce, and a `RollingUpdate` that briefly runs two
emulators against the same savestate directory corrupts it.

**The GPU has two shapes.** A device plugin (`amd.com/gpu`, `nvidia.com/gpu`,
`gpu.intel.com/i915`) is right on a shared cluster: the scheduler knows the
device is taken. A `hostPath` on `/dev/dri` is simpler on a single-node RKE2
host with no plugin installed, but nothing stops a second GPU workload landing
beside it. The Deployment ships the hostPath shape with the plugin shape
commented; pick one and delete the other.

On RKE2 specifically: the bundled ingress controller is nginx, which is what the
Ingress annotations assume. If you have replaced it, the
`backend-protocol: HTTPS` and `proxy-ssl-verify: off` annotations need their
equivalents in whatever you run — without them the proxy speaks plain HTTP to a
TLS port and every request fails.

## Without `DOCKER_MODS`

Some environments cannot let a container pull at boot. Bake the mod in instead:

```bash
docker build -f deploy/Dockerfile.bundled -t my-registry/flycast-romm:latest .
docker push my-registry/flycast-romm:latest
```

Then drop `DOCKER_MODS` from the environment and use that image. The behaviour
is identical — baking it in is exactly what the loader does at runtime — but you
give up updating the mod without a rebuild.

This is the one artefact here that needs a Docker daemon. The mod image itself
is built by Nix without one, which is why CI can publish it with `skopeo`.

## Building and publishing the mod yourself

```bash
nix build .#docker-mod
skopeo copy --insecure-policy \
  docker-archive:./result \
  docker://ghcr.io/youruser/flycast-romm-integration:latest
```

No Docker daemon at any point. The published blob must stay gzip — the mod
loader verifies it with `tar -tzf` — which is what skopeo does by default.
