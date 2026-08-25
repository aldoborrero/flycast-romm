{
  pkgs,
  perSystem,
  flake,
  ...
}:
let
  inherit (pkgs) lib;

  broker = perSystem.self.default;
  rev = flake.shortRev or flake.dirtyShortRev or "dirty";

  # The mod's payload: the s6 service tree from root/, the static broker, and
  # the Lua half of the command channel. The linuxserver mod loader extracts
  # this straight over the container's / with `cp -R`, so the layout here is
  # the layout it lands in.
  modRoot =
    pkgs.runCommand "flycast-romm-mod-root"
      {
        nativeBuildInputs = [ pkgs.nukeReferences ];
      }
      ''
        mkdir -p "$out"
        cp -r ${../../../root}/. "$out/"
        chmod -R u+w "$out"

        install -Dm0755 ${broker}/bin/romm-broker "$out/usr/local/bin/romm-broker"
        install -Dm0644 ${../../../lua/romm-broker.lua} "$out/defaults/romm-broker.lua"

        # s6 runs these directly, and the Nix store's read-only copy loses nothing
        # but the executable bit if we do not set it here.
        for f in "$out"/etc/s6-overlay/s6-rc.d/*/run \
                 "$out"/etc/s6-overlay/s6-rc.d/*/finish \
                 "$out"/etc/s6-overlay/s6-rc.d/*/*.sh; do
          [ -f "$f" ] && chmod 0755 "$f"
        done

        # nixpkgs patches Go's net, mime and time packages to look up
        # /nix/store/...-{iana-etc,mailcap,tzdata} before the system paths, so the
        # binary carries those as references and buildImage would drag their whole
        # closure into the layer, planting a /nix/store inside somebody else's
        # Debian. Blanking the hashes drops the references; Go then falls through
        # to /etc/protocols, /etc/mime.types and /usr/share/zoneinfo, which the
        # base image has.
        nuke-refs "$out/usr/local/bin/romm-broker"

        # /etc/sudoers.d/romm-broker has to be exactly 0440 or sudo ignores the
        # file, and the Nix store normalises every mode to 0444 or 0555 - so
        # init-flycast-broker sets it at runtime instead of here.
      '';
in
# buildImage, not buildLayeredImage: the linuxserver mod loader reads
# `.layers[0].digest` from the manifest and downloads that blob and nothing
# else (docker-mods.v3, get_blob_sha). A layered image would put most of the
# mod in layers the loader never fetches, and it would fail silently — the mod
# "applies" and half of it is not there.
pkgs.dockerTools.buildImage {
  name = "flycast-romm-integration";
  tag = rev;

  copyToRoot = modRoot;

  config = {
    Labels = {
      "org.opencontainers.image.title" = "flycast-romm-integration";
      "org.opencontainers.image.description" =
        "LinuxServer Docker mod that lets RomM drive Flycast for Dreamcast, NAOMI and Atomiswave";
      "org.opencontainers.image.source" = "https://github.com/aldoborrero/flycast-romm";
      "org.opencontainers.image.licenses" = "GPL-3.0-only";
      "org.opencontainers.image.revision" = flake.rev or flake.dirtyRev or "dirty";
      "org.opencontainers.image.version" = rev;
    };
  };

  # dockerTools pins created = "1970-01-01T00:00:01Z", which is what a
  # content-addressed mod wants: the same inputs produce the same digest, and
  # the loader's "has been previously applied, skipping" stays meaningful.
}
