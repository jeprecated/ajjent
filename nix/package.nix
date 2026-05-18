{
  lib,
  buildGoModule,
  makeWrapper,
  jujutsu,
  fzf,
}:

buildGoModule {
  pname = "jjw";
  version = "0.1.0";

  src = lib.cleanSource ../.;
  vendorHash = "sha256-zq8CrPEfh6zd/E7snrJtgv2ONCCAB9k9+cA28ZhcOpQ=";
  subPackages = [ "." ];
  ldflags = [ "-s" "-w" ];
  doCheck = false;

  nativeBuildInputs = [ makeWrapper ];

  postInstall = ''
    if [ -x "$out/bin/jj-workspace-helper" ]; then
      mv "$out/bin/jj-workspace-helper" "$out/bin/jjw"
    fi
  '';

  postFixup = ''
    wrapProgram "$out/bin/jjw" \
      --prefix PATH : ${lib.makeBinPath [ jujutsu fzf ]}
  '';

  meta = {
    description = "Workspace manager for Jujutsu repositories";
    mainProgram = "jjw";
    platforms = lib.platforms.unix;
  };
}
