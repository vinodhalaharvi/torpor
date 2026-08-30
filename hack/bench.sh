#!/usr/bin/env bash
# =============================================================================
# torpor workbench — four panes, two machines
#
#   bash hack/bench.sh          build the session and attach
#   bash hack/bench.sh kill     tear it down
#
#   ┌─────────────────────────┬─────────────────────────┐
#   │ w10-a logs         MAC  │ mapper            VM    │
#   ├─────────────────────────┼─────────────────────────┤
#   │ w10-b logs         MAC  │ shell             VM    │
#   └─────────────────────────┴─────────────────────────┘
#
# Left column is the two boards. Right column is the edge node. Pane titles
# name the machine, because half the confusion in this project has been not
# knowing which side a command was landing on.
# =============================================================================

set -uo pipefail

SESSION="${TORPOR_SESSION:-torpor}"
VM="${TORPOR_VM:-edge}"
REPO="${TORPOR_REPO:-$HOME/embedded_projects/torpor}"
FW="$REPO/firmware"
W10A_IP="${W10A_IP:-192.168.68.115}"
W10B_IP="${W10B_IP:-192.168.68.116}"

if [ "${1:-}" = "kill" ]; then
  tmux kill-session -t "$SESSION" 2>/dev/null && echo "closed $SESSION"
  exit 0
fi

if tmux has-session -t "$SESSION" 2>/dev/null; then
  echo "reattaching to existing '$SESSION'"
  exec tmux attach -t "$SESSION"
fi

# Each pane ends in an interactive shell rather than dying, so a board that
# drops off WiFi leaves you a prompt instead of an empty pane.
shell_after() {
  printf '%s; printf "\\n[%s exited]\\n"; exec "${SHELL:-/bin/bash}"' "$1" "$2"
}

# --- pane 0: w10-a logs, on the Mac -----------------------------------------
tmux new-session -d -s "$SESSION" -c "$FW" \
  "$(shell_after "esphome logs w10-msg-a.yaml --device $W10A_IP" "w10-a")"
tmux select-pane -t "$SESSION".0 -T "MAC · w10-a"

# --- pane 1: mapper, inside the VM ------------------------------------------
# Foreground so a panic is visible immediately. This is the pane that has to be
# restarted after edgecore restarts — registration is in-memory.
tmux split-window -h -t "$SESSION" \
  "$(shell_after "limactl shell $VM -- sudo /tmp/esphome-mapper --v=4" "mapper")"
tmux select-pane -T "VM · mapper"

# --- pane 2: w10-b logs, on the Mac -----------------------------------------
tmux split-window -v -t "$SESSION".0 -c "$FW" \
  "$(shell_after "esphome logs w10-msg-b.yaml --device $W10B_IP" "w10-b")"
tmux select-pane -T "MAC · w10-b"

# --- pane 3: a shell in the VM ----------------------------------------------
# For journalctl, systemctl, and copying binaries in. Deliberately idle.
tmux split-window -v -t "$SESSION".1 \
  "limactl shell $VM"
tmux select-pane -T "VM · shell"

tmux select-layout -t "$SESSION" tiled
tmux set-option -t "$SESSION" -g pane-border-status top 2>/dev/null
tmux set-option -t "$SESSION" -g pane-border-format ' #{pane_index} #{pane_title} ' 2>/dev/null

# Land in the mapper pane — it is the one worth watching.
tmux select-pane -t "$SESSION".1
tmux attach -t "$SESSION"
