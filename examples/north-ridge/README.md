# A fleet you can plan against without owning it

Six devices covering every case the model distinguishes.

| Device | What it is there to show |
|---|---|
| `gw-north` | mains, WiFi, always reachable — the uninteresting case |
| `field-01` | LoRa only. Refused permanently, not slowly |
| `field-19` | two doors, WiFi currently out of range. Online, capable, still pending |
| `field-33` | four days silent, 8% battery. Unreachable and needs a visit |
| `field-41` | reachable and capable, and below the battery floor |
| `field-22` | stolen, decommissioned, excluded from every count |

```bash
make plan
```

`observed.yaml` uses a fake `ObservedState` kind standing in for live twin
state. It is not a CRD and never will be — it exists so a plan can be run
against a fleet that does not exist yet, which is the point of planning before
buying hardware.

Change `--now` to move time:

```bash
cd controller && go run ./cmd/plan --dir ../examples/north-ridge \
  --now 2026-12-25T02:30:00Z
```

Christmas Day is inside the nightly maintenance window and inside the holiday
change freeze. The rollout reports `BLOCKED`, not `Waiting` — the devices are
available and we have been told not to touch them.
