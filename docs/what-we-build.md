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

### DeviceCapability — types built, not yet populated

Per-transport, and time-varying. `config: true, ota: false` is not a missing
feature; it is a permanent fact about 1.7 kbps inside a 1% duty cycle.
`reachableVia` is observed rather than declared, which is what makes a device
Pending while Online and fully capable.

### FirmwareRollout — types and planner built, controller not

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
