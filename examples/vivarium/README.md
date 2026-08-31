# vivarium

A place to keep devices under observation.

```bash
make vivarium
```

Starts an embedded MQTT broker, connects a fleet of devices to it, and lets
them live. They publish real MQTT on real topics — the mapper cannot tell them
from boards.

## Why not "simulator"

A simulator models behaviour approximately for study. These speak the actual
protocol on an actual broker. "Emulator" is closer but implies reproducing
something instruction for instruction, which is not what happens either.

A vivarium is an enclosure for keeping organisms under near-natural conditions
so you can watch what they do. That is the thing.

## The unfair advantage

A control plane can only ever see what devices report. The vivarium knows what
they are actually doing, so it can tell you when the control plane drew the
wrong conclusion:

```
── ground truth 12:56:35
  DEVICE      RUNNING   REPORTED  BATT   BOOTS  NOTE
  field-07    v42       v42       88     5      boot looping
  field-08    v42       v42       91     5      boot looping
  field-12    v41       v42       77     0      REPORTS A HASH IT IS NOT RUNNING
  field-55    v41       v41       64     0      BRICKED
```

`field-12` is converged from every vantage point in the system. Its twin agrees
with its desired state, the broker round trip is clean, `kubectl` is happy, and
it is running the old firmware. That failure happened on real hardware in this
project — a tone that never played while every signal said success — and it is
the reason the health gate compares against what a device reports it is
*running*.

Without ground truth there is no way to tell whether the gate would have caught
it. That is what this is for.

## Correlated failure is the half that matters

Independent per-device probability is easy and produces a fleet where a canary
tells you nothing about the next device — which is a fleet where a canary is
pointless, and therefore one that cannot test whether the canary works.

Real failures are correlated:

```yaml
cohorts:
  - name: hw-rev-b
    firmwareFailsFor: ["v42"]     # deterministic, not a coin flip
    failureMode: bootLoop

gateways:
  - name: gw-north
    outageEvery: 6h               # forty devices silent in the same second
    outageFor: 40m
```

Bad firmware does not fail 2% of devices at random. It fails every device on
one hardware revision. That is the only case where stopping at the first
failure is right, and therefore the only case that tests `healthGateFailures: 1`.

A gateway outage is the most correlated failure there is. A liveness controller
that reports forty separate `Unreachable` devices has told the truth and told
you nothing.

## Failure modes

Every one has been observed on real hardware here or is a documented failure of
the platforms this replaces.

| Mode | What it does | Why it exists |
|---|---|---|
| `reportsWithoutRunning` | reports the new hash, runs the old | happened here, with a tone that never played |
| `bootLoop` | flashes, reports healthy, then reboots forever | a single immediate check calls this healthy — hence `settleFor` |
| `bricks` | takes the firmware and never speaks again | the failure that costs a truck roll |
| `ignores` | drops the write silently | looks like packet loss, is not |
| `wrongHash` | reports neither the old nor the new | breaks any logic assuming two states |
| `intermittent` | goes quiet, comes back | separates a controller that waits from one that gives up |

Plus per-device `packetLoss` and `jitter`, because nothing real is punctual and
a controller that only works against punctual devices does not work.

## Time

```yaml
speed: 120     # two hours of fleet life per minute
```

The interesting states are slow. A device reporting daily takes four days to
become `Unreachable` at a stale multiplier of three, and nobody debugs a
control plane on that schedule.

## Reproducibility

```yaml
seed: 20260831
```

The most important field. A run you cannot replay produces anecdotes — "it
failed once yesterday" is not a bug report. The seed is mixed with each device
name, so adding a device to the file does not reshuffle every other device's
behaviour. Reproducibility that breaks when you edit the config is not
reproducibility.

## What it does not do

It tests the controller against **your model of failure**. It will not find the
failure you did not think of, which is most of what `docs/gotchas.md` turned
out to be.

Necessary, not sufficient. The bench still matters.
