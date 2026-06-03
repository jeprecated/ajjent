{
  lib,
  buildGoModule,
  makeWrapper,
  jujutsu,
}:

buildGoModule {
  pname = "jjw";
  version = "0.1.0";

  src = lib.cleanSource ../.;
  vendorHash = "sha256-EHNNPQznd9xVHx4M2PqXAQlya7NErWImyBfgnP8T+nc=";
  subPackages = [ "." ];
  ldflags = [ "-s" "-w" ];
  doCheck = false;

  nativeBuildInputs = [ makeWrapper ];

  postInstall = ''
    if [ -x "$out/bin/jj-workspace-helper" ]; then
      mv "$out/bin/jj-workspace-helper" "$out/bin/jjw"
    fi

    install -Dm0644 shell/jjw.bash "$out/share/jjw/shell/jjw.bash"
    install -Dm0644 shell/jjw.zsh "$out/share/jjw/shell/jjw.zsh"
  '';

  postFixup = ''
    wrapProgram "$out/bin/jjw" \
      --prefix PATH : ${lib.makeBinPath [ jujutsu ]}
  '';

  meta = {
    description = "Workspace lifecycle tool for Jujutsu repositories";
    mainProgram = "jjw";
    platforms = lib.platforms.unix;
  };
}
