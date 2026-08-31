# torpor

**A Kubernetes control plane for devices that aren't there.**

*Torpor: reduced metabolic activity. An animal that is dormant, not dead, and
will come back on its own schedule.*

```
$ kubectl get liveness

NAME       TRANSPORT  STATE      SILENT  ASSESSMENT
w10-a      wifi       Online     2s      ReportingOnSchedule
w10-b      wifi       Online     2s      ReportingOnSchedule
field-01   lora       Sleeping   47s     WithinExpectedWakeInterval
```

That third row is the one Kubernetes cannot produce. Silent, expected to be
silent, entirely healthy. A Node in that condition is `NotReady` and its pods
get evicted.

`field-01`'s object contains no IP address, no hostname, no address of any kind:

```yaml
protocol:
  configData:
    gateway: w10-a      # not this device
    nodeID: 2           # one byte, and that is the whole scheme
```

Its temperature was measured by an SHT41, transmitted at 915 MHz, decoded by a
board that happens to have WiFi, and read with `kubectl`.

---

## Status

The honest version, because the difference matters.

| | |
|---|---|
| **V0 — read a value** | running on hardware |
| **V1 — write a value that actuates** | running on hardware |
| **V2 — a device with no IP** | running on hardware |
| **V3 — liveness, capability, rollouts** | written, compiles, unit-tested. Never run against a cluster |

The V3 controllers build and the rollout planner passes tests. No reconcile loop
has touched a live cluster, and no firmware has moved through this system.
Compiling is not working, and this README will say so until it is.

---

## The problem

KubeEdge is genuinely good at what it does: Device CRDs, a twin with desired and
reported state, edge autonomy over a local SQLite cache, reliable delivery over
unstable links, and working mappers for Modbus, OPC UA, BLE and ONVIF.

It stops at the mapper. Every mapper reconciles one property on one device.

The test: **if perfect mappers for every protocol shipped tomorrow, could you
safely update forty devices?** No. Nothing about that would have changed.

Missing across every protocol, not only the exotic ones:

- **A fleet.** Nothing groups devices into an addressable target.
- **Ordering.** No canary, no steps, no gate between them.
- **Firmware as desired state.** No field says what a device should be running,
  nothing reports what it *is* running, so nothing can converge the two.
- **Capability.** Every platform assumes uniform capability and discovers
  otherwise by attempting and timing out. `refused` does not exist as an outcome
  anywhere, because you cannot refuse what you never checked.

KubeEdge is to devices what kubelet is to containers. Deployment and rollout
strategy sit above kubelet, and they are the reason anyone uses Kubernetes
rather than a container runtime. Nothing sits above KubeEdge.

## What torpor adds

### DeviceLiveness

Four states where Kubernetes has two. `Sleeping` is healthy and quiet.
`Unreachable` is deliberately not `Failed` — a node under snow is unreachable
and undamaged, and the word describes our knowledge rather than the hardware.

`expectedInterval` is per-device, because a mains gateway and a battery node
checking in daily are both healthy at wildly different rates. A single
cluster-wide timeout — which is what a Node lease is — cannot express that.

### DeviceCapability

Per-transport, and time-varying.

```yaml
transports:
  - type: lora
    ota: false          # arithmetic, not a gap
  - type: wifi
    availability: opportunistic
    ota: true
reachableVia: [lora]    # observed, and it decays
```

`ota: false` for LoRa is not a missing feature. A 1 MB image at 1.7 kbps is
roughly 80 minutes of continuous airtime, inside a duty cycle permitting about
36 seconds per hour. The arithmetic does not terminate.

### FirmwareRollout

Deployment ergonomics, different semantics. Three outcomes:

```
refused:  field-03  NoOTACapableTransport     permanent — stop asking
pending:  field-19  AwaitingTransportWindow   temporary — ask again later
failed:   field-41  HealthGateFailed          the only one worth waking for
```

`field-19` is the interesting one: **Online, fully capable, and still unable to
take this** — because the transport that is up right now cannot carry firmware.
No commercial platform has a field to express that state.

### The transport ladder

One device, two doors, open at different times. Config goes out over LoRa
tonight. Firmware waits for WiFi. Same object, same rollout.

---

## Borrowed from Kubernetes, and where it breaks

| Kubernetes | torpor | Why it diverges |
|---|---|---|
| Deployment | FirmwareRollout | Same shape, same commands |
| Argo `steps` | `steps: [1,10,50,100]` | Borrowed wholesale |
| ReplicaSet | — | Pods are fungible; a device is on your desk |
| `maxUnavailable` | `maxConcurrent` | You do not control availability. Asleep is asleep |
| Pending | Pending **and** Refused | Kubernetes Pending means "not yet"; some devices mean "never" |
| Readiness probe | `mustReportWithin` | You cannot poll a device that is asleep |

**Like a Deployment, except the target might be asleep and might be incapable.**

---

## Hardware

Two Meshnology W10 boards — ESP32-S3, SX1262 at 915 MHz, SHT41, GPS, display,
audio. Both on ESPHome, both on WiFi, with a bidirectional LoRa link. An
ODROID-C2 for the edge node; a Lima VM stands in for it today.

The same board appears twice: `w10-b` addressed over WiFi, and `field-01`
reached over LoRa through `w10-a`. Deliberate — it proves the difference is in
the model, not the hardware.

## Layout

```
controller/   DeviceLiveness, FirmwareRollout, the planner — our own APIs
mapper/       the DMI mapper: a protocol adapter, like a CSI driver
firmware/     ESPHome configs
manifests/    DeviceModel, Device, CRDs, RBAC
docs/         roadmap, protocol matrix, and gotchas — read that one
hack/         tmux workbench, first-push safety checks
```

## Start here

```bash
cp firmware/secrets.yaml.example firmware/secrets.yaml   # then edit it
make check            # every hop in the chain, with a tick or a cross
make v0               # a temperature, via kubectl
make v2               # the device with no address
make bench            # four-pane workbench
```

`make` alone prints grouped help.

## A fleet without a fleet

```bash
make vivarium
```

Starts an embedded MQTT broker and a fleet of devices on it. They speak real
MQTT on real topics — the mapper cannot tell them from boards.

The unfair advantage is that it knows ground truth:

```
── ground truth
  DEVICE      RUNNING   REPORTED  BATT  BOOTS  NOTE
  field-07    v42       v42       88    5      boot looping
  field-12    v41       v42       77    0      REPORTS A HASH IT IS NOT RUNNING
  field-55    v41       v41       64    0      BRICKED
```

`field-12` is converged from every vantage point in the system — twin agrees,
broker round trip clean, `kubectl` happy — and it is running the old firmware.
That failure happened here on real hardware, and without ground truth there is
no way to check whether the health gate would have caught it.

Failures are correlated, which is the half that matters: a cohort on hardware
revision B all boot-loop on the same image, and a gateway outage takes forty
devices silent in the same second. Independent probability produces a fleet
where a canary tells you nothing about the next device — which cannot test
whether the canary works.

See `examples/vivarium/README.md`.

## Try it without hardware

```bash
make plan
```

Reads manifests, runs the same assessment code the controllers run, and prints
every decision. Writes nothing.

```
torpor plan  2026-08-31 04:00 UTC  —  5 devices, 1 excluded as decommissioned

── rollouts ───────────────────────────────────────────────
  sensors-v42  target=5 eligible=1 refused=3 pending=1
    canary would be: gw-north
    REFUSED field-01  ArtifactExceedsTransportCapacity  lora — none can carry firmware
    REFUSED field-41  OTAExceedsBatteryBudget           18% remaining, floor is 25%
    PENDING field-19  AwaitingTransportWindow           reachable via [lora], none OTA-capable

── credentials ────────────────────────────────────────────
  field-sensor-certs  healthy=3 expiring=0 atRisk=2 expired=0
    AtRisk  field-01  left=59d20h  via=[]  schedule site visit before 2026-10-30
```

Every object here exists to make a decision *before* anything is transmitted.
A tool that shows those decisions before you commit to them is the natural
interface to a system built on refusing early.

It shares code with the reconcilers rather than reimplementing them — a dry run
that exercises different code from the real thing is a dry run that lies.

## See it all at once

`docs/walkthrough.md` — every object, told as one fleet: forty-seven sensors at
a water utility, three pump houses, most on LoRa. Each object exists because the
one before it left a question unanswered.

## Not tied to ESPHome

Nothing above the mapper knows ESPHome exists. `docs/device-contract.md` is
what a device has to do to join a fleet — five things, four of which are
publishing a string:

1. publish sensor values
2. publish what firmware it is **running**, not what it was told
3. publish a retained birth, and set a will
4. announce itself on boot
5. accept a firmware URL and **pull** from it

ESPHome is the reference implementation because it was what was on the desk. A
Zephyr device on nRF implements the same contract; the first four items are an
afternoon and the fifth is MCUmgr, which is also where the interesting BLE and
Thread DFU work lives.

The honest boundary: a device that cannot report what it is *running* cannot be
managed by this system. Everything else degrades into `Unknown` and `Refused`,
which are answers. That one is not.

## How the objects compose

Nothing references anything by name. A `MaintenanceWindow` does not know which
rollouts it governs; a `FirmwareRollout` does not know a window exists. They
meet through labels on the Device.

That matters organisationally: the person who decides it is unsafe to touch a
pump during the day shift is not the person shipping v42, and neither should
have to read the other's YAML.

The decision chain, and the order determines what an operator is told:

```
1. Capability   can this device EVER take this?    -> Refused   permanent
2. Window       are we permitted right now?         -> Blocked   policy
3. Liveness     is it reachable?                    -> Pending   temporary
4. Contact      will contact last long enough?      -> Pending   arithmetic
5. Transport    which door — rechecked at write time
```

Window before liveness, because a device that is awake during a change freeze
is still off limits — reporting that as "pending, device asleep" sends somebody
to debug a radio when the answer is "frozen until January".

See `docs/composition.md`.

## Where it goes next

`docs/next.md`, ordered by cost against what it settles. The top of that list is
running what already exists — six pull requests of Go were written without a
compiler, and every one looked correct at the time.

The nearest real design question is intent semantics: when three patches land
while a node sleeps, does it get all three in order or only the last? Today the
answer is "only the last" by accident rather than by choice, and firmware and
calibration want opposite answers on the same device.

## docs/gotchas.md

A dozen findings not documented anywhere else, several of them upstream bugs.
The pattern worth extracting: **most of them reported success.**

- CloudCore installs, reports Running, and has no RBAC for the CRD it depends
  on. Every signal green, no twin ever created. (1.23.1, still open.)
- `optimistic: true` echoed a commanded value with the automation never running.
  Twin converged, broker round-tripped, hardware silent.
- ESPHome flashed a cached binary and reported `OTA successful` for firmware
  that had never been compiled.
- A generated config drifted from its source and silently lacked half a feature.

Which produces the constraint the whole V3 layer is built on: **convergence is
not proof of effect.** A health gate checking reported-equals-desired would have
passed every one of those.

## Prior art

Wirepas reaches a similar conclusion — node roles are dynamic, so capability
changes as the mesh reorganises — inside a proprietary stack, for its own mesh.
Independent convergence on the same structure is evidence the structure is real.

So the claim is narrower than "nobody models this": no open, transport-agnostic
platform models it, and the closed stack that does confines it to one protocol.

## License

MIT. Do what you want with it.

Chosen over Apache 2.0 deliberately. Apache's patent grant would be worth
having in a space with existing commercial products, and giving it up is a real
trade — but the point of this being open is that somebody with a real fleet
tries it, and the shortest licence is the one nobody has to send to legal
first.

## Built on

[KubeEdge](https://kubeedge.io) (CNCF graduated) for the device substrate,
[ESPHome](https://esphome.io) for firmware, and Argo Rollouts for the rollout
ergonomics. None of the substrate is reimplemented on purpose — the thesis is
that the layer above it is missing, not that the layer below is wrong.
