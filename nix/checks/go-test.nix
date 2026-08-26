{ perSystem, ... }:
# The package build skips its tests so it stays fast; this runs them, and a
# failure shows up as `checks.<system>.go-test` rather than as a broken build.
perSystem.self.default.overrideAttrs (old: {
  pname = "${old.pname}-tests";
  doCheck = true;
  # The race detector needs cgo. The shipping binary stays CGO_ENABLED=0 in
  # the package build; this derivation only exists to run the tests, and a
  # broker this concurrent should never pass CI without -race.
  env = (old.env or { }) // {
    CGO_ENABLED = 1;
  };
  # buildGoModule's default checkPhase only tests the subPackages it builds.
  # Everything under internal/ needs testing too.
  checkPhase = ''
    runHook preCheck
    go test -race -count=1 ./...
    runHook postCheck
  '';
})
