{ pkgs, inputs, ... }:
(inputs.treefmt-nix.lib.evalModule pkgs {
  projectRootFile = "flake.nix";

  programs = {
    gofumpt.enable = true;
    nixfmt.enable = true;
    prettier.enable = true;
    shfmt.enable = true;
    stylua.enable = true;
  };

  settings.formatter = {
    # s6 service definition files are content-addressed by name, not extension:
    # `run`, `up`, `finish` are shell scripts with no suffix.
    shfmt.includes = [
      "*/s6-rc.d/*/run"
      "*/s6-rc.d/*/up"
      "*/s6-rc.d/*/finish"
      "*.sh"
    ];
    prettier.excludes = [ "flake.lock" ];
  };

  settings.global.excludes = [
    "LICENSE"
    "*.lock"
    # These have no trailing newline and no shebang, and s6 reads them literally.
    "*/s6-rc.d/*/type"
    "*/s6-rc.d/*/dependencies.d/*"
    "*/s6-rc.d/user/contents.d/*"
    "*/sudoers.d/*"
  ];
}).config.build.wrapper
