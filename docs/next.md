# What's next

Ordered by what it costs against what it settles. The top of this list is
cheap and load-bearing; the bottom is expensive and speculative.

A rule that has earned itself over the last day: **nothing below "run it" is
worth building until the things above it have run.** Six pull requests of Go
were written without a compiler, and every one of them looked correct at the
time.

---

## 0. Run what exists

Nothing else on this list means anything first.

- `go build ./...` in `mapper/esphome` — the multi-transport driver has never
  been compiled. The controllers have; the mapper has not.
- Both controllers against the live cluster. Capability derivation has never
  seen a real Device object.
- **The liveness demo.** Unplug `w10-b`, watch `field-01` walk Online →
  Sleeping → Unreachable while `w10-a` stays green. That is the row the whole
  project is named after, and it has never been observed.
- **The OTA path.** `make serve-fw`, then a rollout at `w10-b`. Does a
  `kubectl` write make a board flash itself?
- **The ladder.** Both transports up, unplug WiFi, plug it back in. Does
  `firmware_url` defer with a reason and then land?

Until the ladder test passes, everything below is design rather than
engineering.

---

## 1. Intent semantics: Collapse or Ordered

The question the V2 roadmap asked and nothing has answered. Currently the twin
holds one desired value per property, so the answer is **Collapse** — by
accident, not by choice.

Collapse is usually right. `sample_interval` set to 300, then 600, then 300
again while a node sleeps six hours: the intermediate values were superseded
and replaying them is pointless.

Collapse is sometimes catastrophic. A calibration sequence, or a config
migration where step 2 is only valid once step 1 has applied, is silently
destroyed — and nothing reports an error, because from the twin's perspective
it converged. That is the same failure mode as every other bug in
`gotchas.md`: success reported, nothing achieved.

Firmware makes it sharp. Three rollouts land while a node is out of range.
Does it wake and flash v40, v41, v42 — three flashes, three reboots, three
chances to brick — or jump to v42? Almost certainly the latter. But that is a
*decision*, and it means firmware and calibration want different semantics on
the same device.

```yaml
properties:
  - name: sample_interval
    intent:
      mode: Collapse            # default: only the last value survives

  - name: calibration_step
    intent:
      mode: Ordered             # every value delivered, in order
      queue:
        - {value: "zero",  at: "2026-08-30T09:00:00Z"}
        - {value: "span",  at: "2026-08-30T09:05:00Z"}
      maxQueueDepth: 8          # bounded; a queue that grows without limit
                                # on a node asleep for a month is a leak
```

Default Collapse, because it is the cheap and safe answer and matches what
already happens. Ordered where sequence is load-bearing, and it needs a bound —
an unbounded queue behind a device that sleeps for a month is a memory leak
with a plausible excuse.

Open question worth settling before implementing: **when an Ordered queue
overflows, what happens?** Drop the oldest and lose the sequence's beginning,
drop the newest and lose the operator's most recent intent, or refuse the write
and surface it. Refusing is probably right — it is the only option that does
not silently discard something someone asked for — but it means writes can fail
for a reason that has nothing to do with the device.

---

## 2. Topology as an ordering constraint

Third scheduling constraint, after airtime and bus contention.

In a mesh, updating a router orphans its children mid-transfer. `steps` and
`maxConcurrent` cannot express this: the constraint is not how many at once, it
is *which before which*.

```yaml
strategy:
  topology:
    order: leavesFirst
    constraint: preserveRouterConnectivity
```

Kubernetes has affinity and topology spread, but they govern **placement**.
This is topology as an ordering constraint on **mutation**, and there is no
analogue — pods do not route traffic for each other in a physical mesh.

Applies to Thread, Zigbee and Wi-SUN alike, so it is not a Thread feature. Needs
three radios to demonstrate: one router, two children.

---

## 3. Power-mode transitions during transfer

A Thread sleepy end device must set `poll_period: 0` for the duration of an
OTA and restore it after. It stops being low-power while updating.

The gap is not the transition, it is the failure path. **A rollout that dies
mid-transfer leaves a battery node in high-power mode until somebody notices** —
and nothing currently notices, because from the controller's perspective the
rollout simply stopped.

That needs the controller to own a restore obligation across its own crash,
which is a different shape from anything here so far. Everything else in this
project is level-triggered and idempotent; this is not.

---

## 4. Scaling: shared subscriptions and address space

Correct at 3, wrong at 3000. Three separate problems:

- **Fan-out.** Every LoRa device opens its own MQTT connection to the same
  broker and subscribes to the same `<gateway>/lora/rx`, then discards every
  frame that is not its own. 5000 devices behind one gateway is 5000
  connections and every frame delivered 5000 times. Needs one subscription per
  gateway with a fan-out table.
- **Address space.** `nodeID` is one byte of the wire protocol. 255 devices.
  Widening it is a frame format change and costs airtime, which at SF9 is the
  scarcest thing there is.
- **Airtime budget.** One gateway supports roughly 360 seconds of airtime per
  hour across all its nodes. At 30-second intervals that is about 100 nodes.
  5000 means much longer intervals, more gateways, or both.

The first two are engineering and can wait. **The third is not optimization —
it is the thesis.** A controller that plans against duty cycle before
transmitting is the thing nobody else does, and deferring it indefinitely turns
this into a device management tool.

---

## 5. Protocol coverage

In rough order of what each teaches:

- **Thread**, on the C6. The only transport that is both low-power *and*
  firmware-updatable, which makes it the only place the battery-cost refusal
  has anything to bite on. Verify ESPHome's `openthread:` supports
  `http_request` OTA before assuming; if not, that is a component worth
  writing and contributing upstream.
- **Modbus**, using KubeEdge's existing mapper. Costs nothing and proves the
  claim that the fleet layer is protocol-agnostic — `DeviceLiveness` works
  unchanged on a Modbus device today. Also introduces bus contention, which is
  LoRa duty cycle in different clothes.
- **Zigbee.** Gateway-shaped like LoRa, `config: true, ota: false` in practice
  because no ESPHome OTA component exists. Adds coverage rather than insight.
- **BLE.** Capable in principle, unavailable in practice. Records the
  distinction and little else.

---

## 6. Operational hardening

Unglamorous, and the difference between a demo and something someone would run.

- Controller deployment manifest. It runs locally today.
- Leader election, which is written but never exercised.
- Metrics. `refused_total` by reason is the interesting one — a rising count
  means the fleet's capability assumptions are drifting from reality.
- Events on the Device, so `kubectl describe` shows rollout history where an
  operator would look for it.
- `kubectl rollout undo` actually implemented; `previousConfigHash` is
  recorded and unused.

---

## Deliberately not doing

**Observability.** Prometheus, Grafana, InfluxDB. Every IoT platform has
charts, and building them proves nothing anyone else cannot already show.
KubeEdge's `pushMethod` makes it nearly free whenever a nicer demo backdrop is
wanted; it is not a phase.

**A UI.** Same argument, more work.

**Rebuilding anything KubeEdge does.** The twin, the tunnel, the SQLite cache,
the mappers. That layer is finished and good. The whole bet is that what is
missing sits above it.
