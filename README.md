# torpor

A Kubernetes-native control plane for physical device fleets, built on KubeEdge.

Devices become Kubernetes objects. Desired state is declared with `kubectl` or
GitOps. A controller reconciles it against hardware that may be asleep, on
battery, or reachable only over a radio link.

The differentiated layer is **capability-aware rollouts**: canary, health
gating, rollback, and planning that accounts for devices which can receive
config but not firmware. A rollout targeting an OTA-incapable node is refused,
not attempted and timed out. No commercial platform models this, and nothing
open source is both self-hosted and mesh-aware.

## Status

V0 — read one number through the full chain.

    kubectl get device w10-a -o jsonpath='{.status.twins[0].reported.value}'

See `docs/roadmap-kubectl.md`. Each version is defined by what you can type.

## Layout

    docs/       architecture, roadmap, protocol capability matrix
    firmware/   ESPHome configs for the Meshnology W10 boards
    manifests/  DeviceModel and Device CRs
    mapper/     the DMI mapper — protocol adapter, like a CSI driver
    Makefile    day-to-day ops

## Setup

Copy `firmware/secrets.yaml.example` to `firmware/secrets.yaml` and fill it in.
Then `make check`.
