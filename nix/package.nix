{
  lib,
  buildGoModule,
  makeWrapper,
  jujutsu,
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
  ];
  doCheck = true;
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
