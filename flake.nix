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
  };

  outputs =
    inputs:
    inputs.blueprint {
      inherit inputs;
      prefix = "nix/";
      systems = import inputs.systems;
    };
}
