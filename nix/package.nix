{ pkgs, flake, ... }:
let
  inherit (pkgs) lib;
in
pkgs.buildGoModule {
  pname = "flycast-romm-broker";
  version = "0.1.0-" + (flake.shortRev or flake.dirtyShortRev or "dirty");

  # Only the sources the compiler reads, so editing a document or a deploy
  # manifest does not invalidate the binary and, through it, the mod image.
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../cmd
      ../internal
    ];
  };

  # Stdlib only. A broker that lives inside somebody else's Debian container
  # cannot afford a dependency tree, and there is nothing here the standard
  # library does not already do.
  vendorHash = null;

  subPackages = [ "cmd/broker" ];

  # Static, because a Docker mod can only copy files: the binary lands in a
  # container whose libc is not the one Nix built against, and nothing else
  # from the closure comes with it.
  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${flake.shortRev or flake.dirtyShortRev or "dirty"}"
  ];

  # The tests run as their own check (nix/checks/go-test.nix) so a package
  # build stays fast and a test failure names itself in `nix flake check`.
  doCheck = false;

  postInstall = ''
    mv "$out/bin/broker" "$out/bin/romm-broker"
  '';

  meta = {
    description = "HTTP broker that lets RomM drive Flycast in a linuxserver container";
    homepage = "https://github.com/aldoborrero/flycast-romm";
    license = lib.licenses.gpl3Only;
    mainProgram = "romm-broker";
    platforms = lib.platforms.linux;
  };
}
