# How the objects compose

Nothing here references anything by name. A `MaintenanceWindow` does not know
which rollouts it governs. A `FirmwareRollout` does not know a window exists.
They meet through labels on the Device, the way a `NetworkPolicy` and a `Pod`
do.

That matters organisationally rather than technically. The person who decides
it is unsafe to touch a pump during the day shift is not the person shipping
v42, and neither should have to read the other's YAML for the system to behave
correctly.

```
                    Device
              labels: {role: field-sensor, site: north-ridge}
                       │
        ┌──────────────┼──────────────┬─────────────────┐
        │              │              │                 │
  DeviceLiveness  MaintenanceWindow  FleetDrift  CredentialExpiry
   (derived)        (ops writes)      (derived)     (derived)
        │              │
        └──────┬───────┘
               │
         FirmwareRollout
       (release writes; knows about neither)
```

## Who writes what

| Object | Author | Cadence |
|---|---|---|
| `Device` | whoever owns the hardware | once, at provisioning |
| `MaintenanceWindow` | operations | rarely, and it outlives every rollout |
| `FirmwareRollout` | whoever ships firmware | per release |
| `DeviceLiveness` | nobody — derived | continuously |
| `FleetDrift` | nobody — derived | continuously |
| `CredentialExpiry` | nobody — derived | continuously |

Three of six are never written by a human. That is the intended ratio: a
control plane that requires people to keep state up to date will have stale
state.

## The decision chain

For one device, in this order:

```
1. Capability   can this device EVER take this?     -> Refused   permanent
2. Window       are we permitted to act right now?   -> Blocked   policy
3. Liveness     is it reachable?                     -> Pending   temporary
4. Contact      will contact last long enough?       -> Pending   arithmetic
5. Transport    which door — rechecked at write time
```

**The order is not an optimisation.** It determines what an operator is told,
and the wrong order sends them somewhere useless.

**Capability first**, because "never" beats "not now". A LoRa-only node is
refused in December and still refused in January, and an operator should learn
that during the freeze rather than after it lifts.

**Window before liveness**, because a device that is awake during a change
freeze is still off limits. Reporting that as "pending, device asleep" sends
somebody to debug a radio when the answer is "frozen until the 5th".

**Transport last, and twice.** The planner picks a transport at decision time
and the mapper picks one again at write time, because a plan can be minutes
stale and the door that was open when the rollout decided may not be open now.
The write path is the check that is actually true.

## What an operator sees

```
$ kubectl get firmwarerollout sensors-v42

NAME          TARGET  ELIGIBLE  UPDATED  HEALTHY  PHASE    STEP
sensors-v42   47      31        0        0        Blocked  1/4
```

```yaml
status:
  blockedBy: field-sensors-nightly
  blockReason: holiday change freeze
  nextWindow: "2027-01-05T02:00:00Z"
  refused:
    - device: field-03
      reason: NoOTACapableTransport
```

Both facts are present and they answer different questions. *Why is nothing
happening* — a change freeze until January 5th. *What will still be wrong
afterwards* — eleven devices that can never take this at all.

`blockedBy` and `nextWindow` live on the rollout rather than only on the window
because "why is nothing happening" is the question this system generates, and
the answer should be in the object the person is already looking at.

## Phases, and why Blocked is not Waiting

| Phase | Meaning |
|---|---|
| `Blocked` | We are not permitted to act. Policy. The devices may be fine. |
| `Waiting` | We are permitted and nothing can proceed. Circumstance. |
| `Paused` | A health gate failed. A human should look. |

Collapsing `Blocked` into `Waiting` would be the same class of error as
collapsing `Refused` into `Failed`: it takes a situation with a clear owner and
a known resolution time and reports it as an unexplained absence.

## Composition rules

**Deny beats Allow, across objects as well as within one.** Overlapping
policies are normal — a site-wide nightly window and a company-wide holiday
freeze — and if any of them says no, the answer is no. A policy that could be
escaped by adding another policy is not a policy.

**No windows configured means always permitted.** Right for a bench, wrong for
a water utility, which is why the object is opt-in rather than defaulted.

**`maxDevicesPerWindow` is a ceiling on the strategy, not a negotiation.** A
rollout asking for 50 devices inside a window permitting 10 gets 10. It is a
blast-radius limit: if the image turns out bad, that is how many devices break
before somebody has a chance to notice.
