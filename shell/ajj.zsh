# Source this file from ~/.zshrc after ajj is on PATH.
# It wraps navigation commands so they can cd the current shell.

ajj() {
  local out rc cmd
  case "$1" in
    --repo)
      cmd="$3"
      ;;
    --repo=*)
      cmd="$2"
      ;;
    *)
      cmd="$1"
      ;;
  esac
  case "$cmd" in
    create|open|close|main)
      out="$(AJJ_SHELL_WRAPPED=1 command ajj "$@")"
      rc=$?
      if [ $rc -ne 0 ]; then
        return $rc
      fi
      if [ -n "$out" ]; then
        cd "$out" || return
      fi
      ;;
    *)
      command ajj "$@"
      ;;
  esac
}
