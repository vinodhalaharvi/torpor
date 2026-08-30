# =============================================================================
# esphome tmux helpers — append to ~/.bashrc
#
#   eht w10-a.yaml w10-b.yaml          build sequentially, then watch in panes
#   eht w10-a w10-b                    .yaml is optional
#   eht w10-msg-a.yaml w10-msg-b.yaml
#   eht a.yaml b.yaml c.yaml           any number of panes
#
#   ehl <same args>                    logs instead of run
#   ehk                                kill the session
#
# Pane titles come from the filename with .yaml stripped.
# =============================================================================

ESPHOME_DIR="${ESPHOME_DIR:-$HOME/esphome}"
ESPHOME_SESSION="esph"

_esph_tmux() {
  local cmd="$1"; shift
  [ $# -eq 0 ] && { echo "usage: ${cmd} FILE [FILE ...]" >&2; return 1; }

  tmux kill-session -t "$ESPHOME_SESSION" 2>/dev/null

  local first=1
  for f in "$@"; do
    local base="${f%.yaml}"                       # strip extension if given
    if [ ! -f "$ESPHOME_DIR/$base.yaml" ]; then
      echo "not found: $ESPHOME_DIR/$base.yaml" >&2
      tmux kill-session -t "$ESPHOME_SESSION" 2>/dev/null
      return 1
    fi

    if [ $first -eq 1 ]; then
      tmux new-session -d -s "$ESPHOME_SESSION" -c "$ESPHOME_DIR" \
        "esphome $cmd $base.yaml; echo; echo '[$base done — any key]'; read -n1"
      tmux select-pane -t "$ESPHOME_SESSION".0 -T "$base"
      first=0
    else
      tmux split-window -h -t "$ESPHOME_SESSION" -c "$ESPHOME_DIR" \
        "esphome $cmd $base.yaml; echo; echo '[$base done — any key]'; read -n1"
      tmux select-pane -T "$base"
    fi
  done

  tmux select-layout -t "$ESPHOME_SESSION" even-horizontal
  tmux attach -t "$ESPHOME_SESSION"
}

# Build and upload each config one at a time, then open the logs side by side.
#
# The builds are sequential on purpose. ESPHome shares one component cache
# across builds, and two compiles fetching the same managed component at once
# will race — one of them finds half-written files and dies with
# "Component discovery failed". tmux is for watching, not for building.
eht() {
  [ $# -eq 0 ] && { echo "usage: eht FILE [FILE ...]" >&2; return 1; }

  local names=()
  for f in "$@"; do
    local base="${f%.yaml}"
    if [ ! -f "$ESPHOME_DIR/$base.yaml" ]; then
      echo "not found: $ESPHOME_DIR/$base.yaml" >&2
      return 1
    fi
    names+=("$base")
  done

  tmux kill-session -t "$ESPHOME_SESSION" 2>/dev/null

  ( cd "$ESPHOME_DIR" || return 1
    for n in "${names[@]}"; do
      echo
      echo "=================== building $n ==================="
      esphome run "$n.yaml" --no-logs || {
        echo "FAILED: $n — not opening logs" >&2
        return 1
      }
    done
  ) || return 1

  echo
  echo "all uploaded — opening logs"
  sleep 2
  _esph_tmux logs "${names[@]}"
}

# logs only
ehl() { _esph_tmux logs "$@"; }

ehk() { tmux kill-session -t "$ESPHOME_SESSION" 2>/dev/null && echo "closed"; }
