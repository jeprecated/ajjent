{
  lib,
  buildGoModule,
  makeWrapper,
  jujutsu,
}:

let
  version = "0.1.0";
in
buildGoModule {
  pname = "ajjent";
  inherit version;

  src = lib.cleanSource ../.;
  vendorHash = "sha256-EHNNPQznd9xVHx4M2PqXAQlya7NErWImyBfgnP8T+nc=";
  subPackages = [ "." ];
  ldflags = [
    "-s"
    "-w"
    "-X"
    "main.version=${version}"
  ];
  doCheck = false;

  nativeBuildInputs = [ makeWrapper ];

  postInstall = ''
    if [ -x "$out/bin/ajjent" ]; then
      mv "$out/bin/ajjent" "$out/bin/ajj"
    fi

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
