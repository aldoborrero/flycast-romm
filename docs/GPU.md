# GPU acceleration

By default the streaming stack renders and encodes on the CPU. On a machine
with a usable GPU you can move both to hardware, which is the difference between
"a light game runs" and "games run at full speed". On an AMD Radeon 780M
(Phoenix APU) in an LXC, enabling the full GPU path took Crazy Taxi from
**~25 FPS to a locked 60**, and dropped whole-machine CPU from **~85% to ~41%**.

This is entirely userspace configuration — the kernel passthrough (a `/dev/dri`
render node reachable inside the container) is a prerequisite, not something
this mod configures. What follows is what has to be in place *around* the
emulator and compositor.

## The chain, and where each piece runs on hardware

1. **Compositor (selkies/pixelflux)** renders on the GPU and exposes
   `zwp_linux_dmabuf_v1`. Without a GPU it falls back to a software (Pixman)
   compositor that only exposes `wl_shm`, and then nothing downstream can use
   the GPU.
2. **Flycast** renders with Vulkan (RADV) into a dmabuf the compositor accepts.
3. **selkies (pcmflux/pixelflux)** encodes H.264 with VAAPI, zero-copy from the
   same dmabuf.

If the compositor is software, both Flycast's Vulkan swapchain
(`ErrorSurfaceLostKHR`) and hardware encode fail and silently fall back to
CPU — so the compositor coming up on the GPU is the linchpin.

## Environment (the non-obvious part)

These must be present in the environment the compositor **and** the emulator
run in. On a Nix host the paths are store paths; substitute your own.

| Variable | Why |
|---|---|
| `LD_LIBRARY_PATH` includes **glvnd** (`libEGL.so.1`) + mesa + wayland | pixelflux `dlopen`s `libEGL.so.1` and `libwayland-server.so.0` by name; missing glvnd is the classic `Failed to load LibEGL` panic that drops it to software |
| `GBM_BACKENDS_PATH=<mesa>/lib/gbm` | Mesa looks for its GBM backend under `/run/opengl-driver/lib/gbm/` by default, which does not exist off NixOS; without this the compositor cannot create a GBM device |
| `LIBVA_DRIVER_NAME=radeonsi`, `LIBVA_DRIVERS_PATH=<mesa>/lib/dri` | so `libva` finds the Mesa VAAPI driver for hardware encode |
| `__EGL_VENDOR_LIBRARY_DIRS=<mesa>/share/glvnd/egl_vendor.d` | glvnd's EGL vendor ICD lookup |
| `VK_ICD_FILENAMES=<mesa>/.../radeon_icd.x86_64.json` | so Flycast's Vulkan (RADV) finds the AMD ICD |

The broker forwards these to Flycast automatically: it passes through the
`LD_*`, `LIBVA_`, `MESA_`, `VK_`, `GALLIUM_`, `LIBGL_`, `DRI_`, `__EGL`, `__GL`,
`__NV`, `NVIDIA_` and `GBM_` prefixes (see `internal/flycast/runner.go`), plus
the `LD_PRELOAD` for the joystick interposer. Set them in the container/service
environment and they reach the emulator.

## Flags

- **selkies**: `--use-cpu false` (so its compositor initialises on the GPU and
  its encoder uses VAAPI) and `--dri-node /dev/dri/renderD128`.
- **Flycast**: `-config config:pvr.rend=4` (Vulkan renderer). Note Flycast's
  `-config` format is strictly `section:key=value`.

## Verifying

- `vainfo --display drm --device /dev/dri/renderD128` should list
  `VAProfileH264…: VAEntrypointEncSlice` (hardware H.264 encode present).
- The selkies/pixelflux log should say it initialised a GL renderer on
  `renderD128`, **not** "Falling back to Software Renderer (Pixman)".
- The Flycast log should show `VulkanRenderer::Init` on the AMD device, not
  `Falling back to OpenGL` → `llvmpipe`.
