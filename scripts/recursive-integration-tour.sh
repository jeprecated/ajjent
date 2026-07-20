#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/recursive-integration-tour.sh [--root PATH] [--ajj PATH] [--force]

Create a disposable recursive-integration tour fixture and stop before effects.

Options:
  --root PATH  fixture root (default: /tmp/ajj-recursive-integration-tour)
  --ajj PATH   use this Ajj executable instead of building from this checkout
  --force      remove and deterministically recreate an existing fixture root
  -h, --help   show this help
EOF
}

fail() {
  printf 'recursive-integration-tour: %s\n' "$*" >&2
  exit 2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

quote_export() {
  local name=$1 value=$2 quoted
  printf -v quoted '%q' "$value"
  printf 'export %s=%s\n' "$name" "$quoted"
}

validate_ajj() {
  local binary=$1 capabilities
  capabilities=$("$binary" capabilities --json 2>/dev/null) || fail "Ajj executable does not provide machine capabilities: $binary"
  [[ $capabilities == *'"schema":"ajj-capabilities-v1"'* ]] || fail "Ajj executable has an incompatible capabilities schema: $binary"
  [[ $capabilities == *'"minimumJjVersion":"0.41.0"'* ]] || fail "Ajj executable does not advertise the required jj 0.41 contract: $binary"
  [[ $capabilities == *'"executableStrategies":["single","provider-default","ordered-line"]'* ]] || fail "Ajj executable does not support all tour strategies: $binary"
}

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd -P)
while [[ $REPO_ROOT == //* ]]; do REPO_ROOT=${REPO_ROOT#/}; done
TOUR_ROOT=/tmp/ajj-recursive-integration-tour
AJJ_SOURCE=
FORCE=false

while (($#)); do
  case $1 in
    --root)
      (($# >= 2)) || fail "--root requires a path"
      TOUR_ROOT=$2
      shift 2
      ;;
    --ajj)
      (($# >= 2)) || fail "--ajj requires a path"
      AJJ_SOURCE=$2
      shift 2
      ;;
    --force)
      FORCE=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

STAGING_DIR=
TOUR_ROOT_OWNED=false
cleanup() {
  local status=$?
  trap - EXIT INT TERM HUP
  if [[ -n $STAGING_DIR && ( -e $STAGING_DIR || -L $STAGING_DIR ) ]]; then
    rm -rf -- "$STAGING_DIR"
  fi
  if ((status != 0)) && $TOUR_ROOT_OWNED && [[ -n $TOUR_ROOT ]]; then
    rm -rf -- "$TOUR_ROOT"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

require_command bash
require_command jj
require_command cp
require_command rm
require_command mkdir
require_command mktemp

JJ_VERSION_OUTPUT=$(jj --version 2>/dev/null) || fail "could not execute jj --version"
if [[ ! $JJ_VERSION_OUTPUT =~ ([0-9]+)\.([0-9]+)(\.[0-9]+)? ]]; then
  fail "could not parse jj version from: $JJ_VERSION_OUTPUT"
fi
JJ_MAJOR=${BASH_REMATCH[1]}
JJ_MINOR=${BASH_REMATCH[2]}
if ((JJ_MAJOR == 0 && JJ_MINOR < 41)); then
  fail "jj 0.41.0 or newer is required; found $JJ_VERSION_OUTPUT"
fi

if [[ -n $AJJ_SOURCE ]]; then
  [[ -f $AJJ_SOURCE && -x $AJJ_SOURCE ]] || fail "--ajj is not an executable file: $AJJ_SOURCE"
  AJJ_SOURCE=$(cd -- "$(dirname -- "$AJJ_SOURCE")" && printf '%s/%s\n' "$PWD" "$(basename -- "$AJJ_SOURCE")")
  validate_ajj "$AJJ_SOURCE"
else
  require_command go
  [[ -f $REPO_ROOT/go.mod && -d $REPO_ROOT/cmd/ajj ]] || fail "cannot locate the Ajj source checkout"
fi

case $TOUR_ROOT in
  /*) ;;
  *) fail "--root must be an absolute path: $TOUR_ROOT" ;;
esac
while [[ $TOUR_ROOT != / && $TOUR_ROOT == */ ]]; do
  TOUR_ROOT=${TOUR_ROOT%/}
done
[[ ! -L $TOUR_ROOT ]] || fail "refusing symbolic link fixture root: $TOUR_ROOT"
TOUR_NAME=$(basename -- "$TOUR_ROOT")
case $TOUR_NAME in
  ''|.|..|/) fail "refusing unsafe fixture root: $TOUR_ROOT" ;;
esac
TOUR_PARENT=$(dirname -- "$TOUR_ROOT")
[[ -d $TOUR_PARENT ]] || fail "fixture parent directory does not exist: $TOUR_PARENT"
TOUR_PARENT=$(cd -- "$TOUR_PARENT" && pwd -P)
while [[ $TOUR_PARENT == //* ]]; do TOUR_PARENT=${TOUR_PARENT#/}; done
TOUR_ROOT=$TOUR_PARENT/$TOUR_NAME
while [[ $TOUR_ROOT == //* ]]; do TOUR_ROOT=${TOUR_ROOT#/}; done
[[ ! -L $TOUR_ROOT ]] || fail "refusing symbolic link fixture root: $TOUR_ROOT"

HOME_ROOT=$HOME
if [[ -d $HOME_ROOT ]]; then
  HOME_ROOT=$(cd -- "$HOME_ROOT" && pwd -P)
  while [[ $HOME_ROOT == //* ]]; do HOME_ROOT=${HOME_ROOT#/}; done
fi
case $TOUR_ROOT in
  /|/tmp|/home|"$HOME_ROOT") fail "refusing unsafe fixture root: $TOUR_ROOT" ;;
esac
if [[ $TOUR_ROOT == "$REPO_ROOT" ]]; then
  fail "refusing unsafe fixture root inside source checkout: $TOUR_ROOT"
fi
case $REPO_ROOT/ in
  "$TOUR_ROOT"/*) fail "refusing unsafe fixture root containing source checkout: $TOUR_ROOT" ;;
esac
case $TOUR_ROOT/ in
  "$REPO_ROOT"/*) fail "refusing unsafe fixture root inside source checkout: $TOUR_ROOT" ;;
esac
if [[ -e $TOUR_ROOT || -L $TOUR_ROOT ]]; then
  $FORCE || fail "fixture root already exists: $TOUR_ROOT (rerun with --force to recreate it)"
fi

# The default build is staged and validated outside TOUR_ROOT. A broken source
# build therefore cannot delete an old forced fixture or create a new partial one.
if [[ -z $AJJ_SOURCE ]]; then
  BUILD_TMP_PARENT=${TMPDIR:-/tmp}
  if [[ ! -d $BUILD_TMP_PARENT ]]; then
    BUILD_TMP_PARENT=/tmp
  fi
  BUILD_TMP_PARENT=$(cd -- "$BUILD_TMP_PARENT" && pwd -P)
  case $BUILD_TMP_PARENT/ in
    "$TOUR_ROOT"/*) BUILD_TMP_PARENT=/tmp ;;
  esac
  STAGING_DIR=$(mktemp -d "$BUILD_TMP_PARENT/ajj-recursive-tour-build.XXXXXX")
  AJJ_SOURCE=$STAGING_DIR/ajj
  printf 'Building Ajj from %s ...\n' "$REPO_ROOT"
  (cd -- "$REPO_ROOT" && go build -o "$AJJ_SOURCE" ./cmd/ajj)
  validate_ajj "$AJJ_SOURCE"
fi

if [[ -e $TOUR_ROOT ]]; then
  rm -rf -- "$TOUR_ROOT"
fi
TOUR_ROOT_OWNED=true
mkdir -p -- "$TOUR_ROOT/bin" "$TOUR_ROOT/home" "$TOUR_ROOT/xdg/ajj"
AJJ=$TOUR_ROOT/bin/ajj
cp -- "$AJJ_SOURCE" "$AJJ"
chmod +x "$AJJ"
validate_ajj "$AJJ"

SETUP_DIAGNOSTICS=$TOUR_ROOT/.setup-command.stderr
run_quiet() {
  if ! "$@" > /dev/null 2> "$SETUP_DIAGNOSTICS"; then
    cat "$SETUP_DIAGNOSTICS" >&2
    rm -f -- "$SETUP_DIAGNOSTICS"
    return 1
  fi
  rm -f -- "$SETUP_DIAGNOSTICS"
}
capture_stdout_quiet() {
  local output
  if ! output=$("$@" 2> "$SETUP_DIAGNOSTICS"); then
    cat "$SETUP_DIAGNOSTICS" >&2
    rm -f -- "$SETUP_DIAGNOSTICS"
    return 1
  fi
  rm -f -- "$SETUP_DIAGNOSTICS"
  printf '%s\n' "$output"
}

# Every fixture-facing tool uses isolated configuration and identity. Nothing
# below reads or modifies the developer's Ajj/JJ user configuration.
export HOME=$TOUR_ROOT/home
export XDG_CONFIG_HOME=$TOUR_ROOT/xdg
export NO_COLOR=1
unset JJ_CONFIG JJ_CONFIG_TOML JJ_USER JJ_EMAIL
(
  cd -- "$TOUR_ROOT"
  run_quiet jj config set --user user.name 'Ajj Tour'
  run_quiet jj config set --user user.email 'ajj-tour@example.invalid'
  run_quiet jj config set --user ui.color never
  run_quiet jj config set --user ui.paginate never
)

WORKSPACES_ROOT=$TOUR_ROOT/workspaces
PROJECT=recursive-tour
MAIN=$WORKSPACES_ROOT/$PROJECT/default
mkdir -p -- "$MAIN"

run_quiet jj git init --colocate "$MAIN"
printf '.ajj/integrations/\n' > "$MAIN/.gitignore"
printf 'A small shared base.\n' > "$MAIN/story.txt"
run_quiet jj -R "$MAIN" file track root:.gitignore root:story.txt
run_quiet jj -R "$MAIN" commit -m 'tour: shared base'

cat > "$XDG_CONFIG_HOME/ajj/config.yaml" <<EOF
workspaces_root: $WORKSPACES_ROOT
project: $PROJECT
workspace_handles:
  - A
  - A1
  - A2
  - A3
  - B
handle_strategy: first-unused
main_workspace: default
stack:
  rebase_mode: auto
  shape: auto
  conflict_strategy: prefer-clean
EOF

full_rev() {
  jj -R "$1" --color=never --no-pager --ignore-working-copy \
    log -r "$2" --no-graph -T 'commit_id ++ "\n"'
}

MAIN_BASE=$(full_rev "$MAIN" 'default@-')
A=$(capture_stdout_quiet "$AJJ" --repo "$MAIN" create A --revision "$MAIN_BASE")
A_CHILD_BASE=$(full_rev "$A" 'A@-')

# Create children before A advances. Ordered-line must therefore anchor their
# independent payloads to A's newer current frontier, not their historical base.
A1=$(capture_stdout_quiet "$AJJ" --repo "$A" create A1 --revision "$A_CHILD_BASE")
A2=$(capture_stdout_quiet "$AJJ" --repo "$A" create A2 --revision "$A_CHILD_BASE")
A3=$(capture_stdout_quiet "$AJJ" --repo "$A" create A3 --revision "$A_CHILD_BASE")
B=$(capture_stdout_quiet "$AJJ" --repo "$MAIN" create B --revision "$MAIN_BASE")

printf 'A advanced after its children were created.\n' > "$A/a-target.txt"
run_quiet jj -R "$A" file track root:a-target.txt
run_quiet jj -R "$A" commit -m 'A: target advanced after child creation'

create_payload() {
  local handle=$1 path=$2 word=$3 filename
  filename=${handle,,}.txt
  printf '%s payload from %s.\n' "$word" "$handle" > "$path/$filename"
  run_quiet jj -R "$path" file track "root:$filename"
  run_quiet jj -R "$path" commit -m "$handle: independent payload"
}
create_payload A1 "$A1" alpha
create_payload A2 "$A2" bravo
create_payload A3 "$A3" charlie

printf 'B is intentionally omitted and must survive every tidy.\n' > "$B/b.txt"
run_quiet jj -R "$B" file track root:b.txt
run_quiet jj -R "$B" commit -m 'B: omitted independent payload'

for path in "$MAIN" "$A" "$A1" "$A2" "$A3" "$B"; do
  run_quiet jj -R "$path" workspace update-stale
done

INITIAL_MAIN_HEAD=$(full_rev "$MAIN" 'default@')
INITIAL_B_HEAD=$(full_rev "$MAIN" 'B@')
ENV_FILE=$TOUR_ROOT/env.sh
{
  printf '# Generated by scripts/recursive-integration-tour.sh; source from Bash.\n'
  printf 'unset JJ_CONFIG JJ_CONFIG_TOML JJ_USER JJ_EMAIL\n'
  quote_export TOUR_ROOT "$TOUR_ROOT"
  quote_export HOME "$HOME"
  quote_export XDG_CONFIG_HOME "$XDG_CONFIG_HOME"
  quote_export NO_COLOR 1
  quote_export AJJ "$AJJ"
  quote_export WORKSPACES_ROOT "$WORKSPACES_ROOT"
  quote_export PROJECT "$PROJECT"
  quote_export MAIN "$MAIN"
  quote_export A "$A"
  quote_export A1 "$A1"
  quote_export A2 "$A2"
  quote_export A3 "$A3"
  quote_export B "$B"
  quote_export INITIAL_MAIN_HEAD "$INITIAL_MAIN_HEAD"
  quote_export INITIAL_B_HEAD "$INITIAL_B_HEAD"
  cat <<'EOF'

_tour_head() {
  jj -R "$1" --color=never --no-pager --ignore-working-copy \
    log -r @ --no-graph -T 'commit_id ++ "\n"'
}

tour_verify_fixture_paths() {
  local path
  for path in "$MAIN" "$A" "$B"; do
    case $path in
      "$TOUR_ROOT"/*) ;;
      *) printf 'path escaped fixture: %s\n' "$path" >&2; return 1 ;;
    esac
  done
}

tour_graph() {
  tour_verify_fixture_paths
  jj -R "$MAIN" --color=never --no-pager log -r 'all()'
}

tour_status() {
  tour_verify_fixture_paths
  "$AJJ" --repo "$MAIN" list
}

tour_make_children_request() {
  local strategy=${1:-ordered-line} request
  case $strategy in
    ordered-line|provider-default) ;;
    *) printf 'strategy must be ordered-line or provider-default\n' >&2; return 2 ;;
  esac
  tour_verify_fixture_paths
  for request in "$A" "$A1" "$A2" "$A3"; do
    [[ -d $request ]] || { printf 'workspace is no longer present: %s\n' "$request" >&2; return 1; }
  done
  request="$TOUR_ROOT/A-children-$strategy.json"
  cat > "$request" <<JSON
{"schema":"ajj-integrate-request-v1","operationId":"tour-A-children-$strategy","target":{"expectedWorkspace":"A","expectedHeadCommit":"$(_tour_head "$A")"},"strategy":"$strategy","payloads":[{"workspace":"A1","expectedHeadCommit":"$(_tour_head "$A1")"},{"workspace":"A2","expectedHeadCommit":"$(_tour_head "$A2")"},{"workspace":"A3","expectedHeadCommit":"$(_tour_head "$A3")"}]}
JSON
  printf '%s\n' "$request"
}

tour_make_main_request() {
  local strategy_tag=${1:-ordered-line} request
  case $strategy_tag in
    ordered-line|provider-default) ;;
    *) printf 'strategy tag must be ordered-line or provider-default\n' >&2; return 2 ;;
  esac
  tour_verify_fixture_paths
  [[ -d $A ]] || { printf 'workspace is no longer present: %s\n' "$A" >&2; return 1; }
  request="$TOUR_ROOT/Main-A-$strategy_tag.json"
  cat > "$request" <<JSON
{"schema":"ajj-integrate-request-v1","operationId":"tour-Main-A-$strategy_tag","target":{"expectedWorkspace":"default","expectedHeadCommit":"$(_tour_head "$MAIN")"},"strategy":"single","payloads":[{"workspace":"A","expectedHeadCommit":"$(_tour_head "$A")"}]}
JSON
  printf '%s\n' "$request"
}

tour_assert_main_unchanged() {
  test "$(_tour_head "$MAIN")" = "$INITIAL_MAIN_HEAD"
}

tour_assert_b_unchanged() {
  test "$(_tour_head "$B")" = "$INITIAL_B_HEAD"
}
EOF
} > "$ENV_FILE"

cat > "$TOUR_ROOT/README.txt" <<EOF
Ajj recursive integration tour fixture

Source the generated environment:
  source $(printf '%q' "$ENV_FILE")

Then follow:
  $REPO_ROOT/docs/recursive-integration-tour.md

This fixture has made no integration effects. A1/A2/A3 and B are independent;
A advanced after its children were created. Reset only with the repository script:
  $(printf '%q' "$SCRIPT_DIR/recursive-integration-tour.sh") --root $(printf '%q' "$TOUR_ROOT") --force
EOF

cat <<EOF
Tour fixture ready; no integration operation has run.

  source $(printf '%q' "$ENV_FILE")
  tour_verify_fixture_paths
  tour_status
  tour_graph

Guide: $REPO_ROOT/docs/recursive-integration-tour.md
Main:  $MAIN
A:     $A
A1-3: $A1, $A2, $A3
B:     $B (intentionally omitted)
EOF
