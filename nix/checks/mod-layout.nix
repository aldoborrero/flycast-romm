{ pkgs, perSystem, ... }:
# The linuxserver mod loader gives no feedback: it extracts a tarball over /
# and moves on. A wrong path, a missing executable bit or a second layer all
# look identical from outside — the mod "applies" and does nothing. This check
# asserts the shape of the image the loader will actually see.
pkgs.runCommand "mod-layout-check"
  {
    nativeBuildInputs = [
      pkgs.jq
      pkgs.file
    ];
    image = perSystem.self.docker-mod;
  }
  ''
    set -euo pipefail
    mkdir -p img && tar -xzf "$image" -C img

    fail() { echo "mod-layout: $*" >&2; exit 1; }

    # docker-mods.v3 reads .layers[0].digest and downloads that blob and
    # nothing else. Anything in a second layer would silently not exist.
    layers=$(jq -r '.[0].Layers | length' img/manifest.json)
    [ "$layers" = "1" ] || fail "image has $layers layers; the mod loader only fetches the first"

    # --delay-directory-restore: the layer's directories carry the Nix store's
    # read-only modes, and tar would otherwise apply them before unpacking
    # what goes inside.
    mkdir -p rootfs
    tar -xf img/*/layer.tar -C rootfs --delay-directory-restore
    root="rootfs"

    # The loader verifies the blob with `tar -tzf`, so the registry copy must
    # be gzip. The archive Nix produces is gzip; skopeo preserves that.
    gzip -t "$image" || fail "image archive is not gzip"

    need_file() { [ -f "$root/$1" ] || fail "missing $1"; }
    need_exec() { need_file "$1"; [ -x "$root/$1" ] || fail "$1 is not executable"; }

    # s6-overlay v3 layout. v2's cont-init.d and services.d are deleted by the
    # loader, so a mod that used them would apply to nothing.
    need_file etc/s6-overlay/s6-rc.d/init-flycast-broker/type
    need_file etc/s6-overlay/s6-rc.d/init-flycast-broker/up
    need_exec etc/s6-overlay/s6-rc.d/init-flycast-broker/init.sh
    need_file etc/s6-overlay/s6-rc.d/svc-romm-broker/type
    need_exec etc/s6-overlay/s6-rc.d/svc-romm-broker/run
    need_exec etc/s6-overlay/s6-rc.d/svc-romm-broker/finish

    [ "$(cat "$root/etc/s6-overlay/s6-rc.d/init-flycast-broker/type")" = "oneshot" ] \
      || fail "init-flycast-broker is not a oneshot"
    [ "$(cat "$root/etc/s6-overlay/s6-rc.d/svc-romm-broker/type")" = "longrun" ] \
      || fail "svc-romm-broker is not a longrun"

    # Without the user bundle entries s6-rc never starts either service.
    need_file etc/s6-overlay/s6-rc.d/user/contents.d/init-flycast-broker
    need_file etc/s6-overlay/s6-rc.d/user/contents.d/svc-romm-broker

    # The oneshot rewrites the labwc autostart, which svc-de reads once at
    # startup. Without this edge the patch lands on disk after the desktop has
    # already read the old file, and fails silently.
    need_file etc/s6-overlay/s6-rc.d/init-services/dependencies.d/init-flycast-broker

    need_file etc/sudoers.d/romm-broker
    need_file defaults/romm-broker.lua
    need_exec usr/local/bin/romm-broker

    # A dynamically linked binary would be built against the wrong libc: the
    # mod is copied into somebody else's Debian, with none of its closure.
    file -b "$root/usr/local/bin/romm-broker" | grep -q "statically linked" \
      || fail "the broker is not statically linked"

    # And it must not drag a /nix/store into that container.
    if [ -e "$root/nix" ]; then fail "the layer contains a /nix tree"; fi
    if grep -qa "/nix/store/[0-9a-df-np-sv-z]\{32\}-" "$root/usr/local/bin/romm-broker"; then
      fail "the broker still references the nix store"
    fi

    # The loader selects a manifest by .platform.architecture, so a mislabelled
    # image is simply never found.
    config=$(jq -r '.[0].Config' img/manifest.json)
    arch=$(jq -r '.architecture' "img/$config")
    [ "$arch" = "${pkgs.go.GOARCH}" ] || fail "image architecture is $arch, expected ${pkgs.go.GOARCH}"

    echo "mod layout OK (1 layer, $arch, statically linked broker)"
    touch "$out"
  ''
