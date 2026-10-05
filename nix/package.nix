{
  lib,
  buildGoModule,
  makeWrapper,
  jujutsu,
  # The one Jujutsu release this build trusts for machine create `noCleanup`.
  # The default is the jj that the check phase runs the tests against and that
  # the wrapper puts first on PATH, so the trusted, tested and runtime jj agree.
  noCleanupJjVersion ? jujutsu.version,
}:

let
  version = "1.0.0";
in
assert lib.assertMsg (lib.versionAtLeast jujutsu.version "0.41.0")
  "ajjent requires jujutsu 0.41.0 or newer";
buildGoModule {
  pname = "ajjent";
  inherit version;

  src = lib.cleanSource ../.;
  vendorHash = "sha256-EHNNPQznd9xVHx4M2PqXAQlya7NErWImyBfgnP8T+nc=";
  subPackages = [ "./cmd/ajj" ];
  ldflags = [
    "-s"
    "-w"
    "-X"
    "main.version=${version}"
    "-X"
    "github.com/jeprecated/ajjent/internal/buildcfg.NoCleanupJJVersion=${noCleanupJjVersion}"
  ];
  doCheck = true;
  checkFlags = [ "-timeout=20m" ];
  nativeCheckInputs = [ jujutsu ];

  nativeBuildInputs = [ makeWrapper ];

  postInstall = ''
    install -Dm0644 shell/ajj.bash "$out/share/ajjent/shell/ajj.bash"
    install -Dm0644 shell/ajj.zsh "$out/share/ajjent/shell/ajj.zsh"
  '';

  postFixup = ''
    wrapProgram "$out/bin/ajj" \
      --prefix PATH : ${lib.makeBinPath [ jujutsu ]}
  '';

  meta = {
    description = "Workspace lifecycle tool for Jujutsu repositories";
    mainProgram = "ajj";
    license = lib.licenses.mit;
    platforms = lib.platforms.unix;
  };
}
