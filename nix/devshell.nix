{ pkgs, perSystem, ... }:
pkgs.mkShell {
  name = "flycast-romm-integration";

  packages = with pkgs; [
    # Broker
    go
    gopls
    gotools
    golangci-lint
    gofumpt
    govulncheck

    # Image plumbing: the mod is pushed with skopeo, inspected with dive.
    skopeo
    dive
    crane

    # Talking to a live container while debugging the contract.
    curl
    jq

    # The input-injection tools the container ships, so the fallback path in
    # docs/CONTRACT.md D2 can be tried by hand without a container.
    xdotool
    wtype

    # Lua side of the command channel.
    lua5_4
    stylua

    perSystem.self.formatter
  ];

  env.CGO_ENABLED = "0";
}
