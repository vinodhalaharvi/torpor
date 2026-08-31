# What exists, and what we add

A running note on where the line falls. Companion to `docs/gotchas.md`, which
records how each of these was learned the hard way.

---

## KubeEdge gives us

Device and DeviceModel CRDs. A twin with desired and reported state. Edge
autonomy via a local SQLite cache. Reliable delivery over unstable links. DMI,
and a mapper framework. Working mappers for Modbus, OPC UA, BLE and ONVIF.

This is the boring 80% and it is genuinely good. Rebuilding it would have cost
months and produced something worse.

## KubeEdge does not give us, and neither does anything else

**A fleet.** Every mapper reconciles one property on one device. Nothing groups
forty devices into one addressable target.

**Ordering.** No canary, no steps, no gate between them. Kubernetes has this
for containers — it is what a Deployment *is*.

**Firmware as desired state.** No field says "this device should be running
image X", nothing reports what it is actually running, and so nothing can
converge the two. A Modbus PLC and a LoRa node are equally unmanaged here.

**Capability.** Every platform assumes uniform capability and discovers
otherwise by attempting and timing out. `refused` does not exist as an outcome
anywhere, because you cannot refuse what you never checked.

The test: if perfect mappers for every protocol shipped tomorrow, could you
safely update forty devices? No. Nothing about that would have changed.

---

## The layer we are adding

### DeviceLiveness — built

Four states where Kubernetes has two. `Sleeping` is healthy and quiet;
`Unreachable` is a statement about our knowledge rather than the hardware.
`expectedInterval` is per-device, because a mains gateway and a battery node
checking in daily are both healthy at wildly different rates — which a single
cluster-wide timeout cannot express.

### DeviceCapability — built

Per-transport, and time-varying. `config: true, ota: false` is not a missing
feature; it is a permanent fact about 1.7 kbps inside a 1% duty cycle.
`reachableVia` is observed rather than declared, which is what makes a device
Pending while Online and fully capable.

### FirmwareRollout — built, untested on hardware

Deployment ergonomics, different semantics. The planner sorts a fleet into
three lists before transmitting anything:

- **Refused** — structurally incapable. Permanent. Stop asking.
- **Pending** — capable, not reachable now. Temporary. Ask again later.
- **Failed** — attempted, did not take. The only one worth waking anybody for.

Those three columns are the product.

---

## What we borrow from Kubernetes, and where it breaks

| Kubernetes | Here | Why it diverges |
|---|---|---|
| Deployment | FirmwareRollout | Same shape, same commands |
| RollingUpdate steps | `steps: [1,10,50,100]` | Borrowed wholesale from Argo Rollouts |
| `rollout undo` | `previousConfigHash` | Same ergonomics |
| ReplicaSet | — | Pods are fungible; a device is on your desk |
| `maxUnavailable` | `maxConcurrent` | You do not control availability. Asleep is asleep |
| Pending | Pending **and** Refused | Kubernetes Pending means "not yet"; some devices mean "never" |
| Readiness probe | `mustReportWithin` | You cannot poll a device that is asleep |
| — | `settleFor` | A device that boots, reports, then boot-loops is not healthy |

One-line pitch: **like a Deployment, except the target might be asleep and
might be incapable.**

---

## Prior art worth naming

Wirepas reaches a similar conclusion — node roles are dynamic, so capability
changes as the mesh reorganises — inside a proprietary stack, for its own mesh.
That is good news rather than bad: independent convergence on the same
structure is evidence the structure is real.

The accurate claim is therefore narrower than "nobody models this." It is that
no open, transport-agnostic platform models it, and the closed stack that does
confines it to one protocol.

---

## The transport table, and why it is code

`controller/internal/transports.go` is `docs/protocol-matrix.md` made
executable. The matrix was written from datasheets and airtime arithmetic
before any of this existed; encoding it means the planner refuses for the same
reason a human would, and the reason is auditable rather than folklore.

The entry that matters:

```go
"lora": {
    Config:           true,
    OTA:              false,   // permanent, not unimplemented
    ThroughputBps:    1_760,
    DutyCyclePercent: "1.0",
}
```

`ota: false` is arithmetic, not a gap. A 1 MB image at 1.7 kbps is roughly 80
minutes of continuous airtime, inside a duty cycle permitting about 36 seconds
per hour. No amount of waiting changes that, which is why the planner answers
**Refused** rather than **Pending** — and why the distinction between those two
words is the product rather than a nicety.

Every entry is a default. A Device can override any of it, because a real
deployment knows things a table cannot: a Thread node with a mains-powered
parent, a LoRa link at SF7 rather than SF9, a gateway with a better antenna.
The table is what to assume when nobody has said otherwise.

---

## The transport ladder

One device, two doors, and the doors are open at different times.

```yaml
transports:
  - type: lora
    gateway: w10-a
    ota: false            # 240 byte frames
    staleAfterSeconds: 90
  - type: wifi
    topicPrefix: w10-b
    ota: true
    staleAfterSeconds: 60
```

Config goes out over LoRa tonight. Firmware waits for WiFi. Same object, same
name, same rollout.

Three questions the mapper asks before every write, and they are genuinely
different questions:

1. **Which doors could carry this at all?** A firmware URL does not fit in a
   240-byte frame. Permanent, structural, and knowable without trying.
2. **Which of those are open right now?** Derived from traffic, and it
   *decays* — `staleAfterSeconds` is what stops a link that died on Tuesday
   from being trusted on Friday.
3. **Does this property demand a specific one?** `requiresTransport: wifi` on
   `firmware_url`, because attempting it over LoRa truncates the URL and fails
   in a way that looks like a radio problem.

The refusal is duplicated in the controller's planner and in the mapper's write
path, deliberately. A plan can be minutes stale by the time it is acted on, and
the transport that was up when the rollout decided may not be up now. The write
path is the check that is actually true.
