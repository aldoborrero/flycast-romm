{
  description = "Sega Dreamcast, NAOMI and Atomiswave streaming for RomM, as a linuxserver Docker mod";

  # Inputs use the `git+https://` form rather than the shorter `github:` one.
  # `github:` resolves refs through api.github.com and downloads codeload
  # tarballs; plenty of CI runners and corporate proxies allow the git wire
  # protocol and nothing else. `git+https://` needs only that, resolves to the
  # same revisions, and keeps the lock reproducible in both environments.
  inputs = {
    nixpkgs = {
      type = "git";
      url = "https://github.com/NixOS/nixpkgs";
      ref = "nixos-unstable";
      shallow = true;
    };

    # Linux only, deliberately: every output here is either a Linux binary or an
    # OCI image destined for a linuxserver container. A Darwin system would
    # evaluate to nothing usable.
    systems = {
      type = "git";
      url = "https://github.com/nix-systems/default-linux";
      shallow = true;
      flake = true;
    };

    blueprint = {
      type = "git";
      url = "https://github.com/numtide/blueprint";
      shallow = true;
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.systems.follows = "systems";
    };

    treefmt-nix = {
      type = "git";
      url = "https://github.com/numtide/treefmt-nix";
      shallow = true;
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # Provisions the flycast-smoke LXC (a bare Debian container with Nix on
    # top): the whole capture stack — weston, the patched selkies, pulseaudio,
    # caddy and the broker — as system-manager systemd services, so the launch
    # is a committed config instead of hand-run one-shots.
    system-manager = {
      type = "git";
      url = "https://github.com/numtide/system-manager";
      shallow = true;
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    inputs:
    inputs.blueprint {
      inherit inputs;
      prefix = "nix/";
      systems = import inputs.systems;
    };
}
