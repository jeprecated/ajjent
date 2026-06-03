# Source this file from ~/.bashrc after jjw is on PATH.
# It wraps navigation commands so they can cd the current shell.

jjw() {
  local out rc
  case "$1" in
    create|open|close|main)
      out="$(command jjw "$@")"
      rc=$?
      if [ $rc -ne 0 ]; then
        return $rc
      fi
      if [ -n "$out" ]; then
        cd "$out" || return
      fi
      ;;
    *)
      command jjw "$@"
      ;;
  esac
}
