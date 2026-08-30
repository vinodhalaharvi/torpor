# The roadmap, as kubectl commands

> **Status.** V0, V1 and V2 are built and running on hardware — see the tags.
> V3 is design, not code: there is no `FirmwareRollout` CRD and no controller
> behind it. The commands in the V3 section below describe what is intended,
> not what runs.
>
> Where a command here differs from what actually worked, the difference is
> noted inline. Most of them are v1beta1 schema changes, and all of them are
> in `docs/gotchas.md`.

Each version is defined by what you can type and what comes back. If the command
works, the version is done.

---

## V0 — read one number

**Goal:** a temperature measured by an SHT41 on your desk is readable with kubectl.

Everything on the Mac. Both boards on USB. Read-only. One property.

```bash
kubectl get devices
```
```
NAME    MODEL              NODE       AGE
w10-a   esphome-w10        mac-edge   4m
```

```bash
kubectl get devicestatus w10-a \
  -o jsonpath='{.status.twins[?(@.propertyName=="temperature")].reported.value}'
```
```
26.8
```

> **Corrected after the fact.** This originally read `kubectl get device`.
> v1beta1 extracted twins into a separate `DeviceStatus` CRD, so the value
> lives on `devicestatus/<name>`, and the desired side is `observedDesired`,
> not `desired`. `device/<name>.status.twins[]` still exists and is always
> empty — a jsonpath against it returns nothing, with no error. See
> `docs/gotchas.md`.

```bash
kubectl describe devicestatus w10-a
```
```
Status:
  Twins:
    Property Name:  temperature
    Reported:
      Value:      26.8
      Metadata:
        Timestamp:  1756312800
        Type:       float
```

**What you wrote:** a mapper with one driver, four methods, reading over HTTP or
the native API. Maybe 200 lines.

**Why this is the whole architecture:** every hop in the chain ran. API server →
CloudCore → WebSocket → EdgeCore → DMI → mapper → device, and all the way back.
Nothing after this changes the shape.

---

## V1 — write one value

**Goal:** the loop closes. You set desired state and the physical thing changes.

```bash
# NOT --type=merge. spec.properties is a list, and a merge patch replaces a
# list rather than merging by name — it deletes every other property and
# strips the visitor config from the one being set, after which the mapper
# panics on the nil visitor. Use `make set`, which locates the index:
i=$(kubectl get device w10-a -o json \
    | jq -r '.spec.properties | to_entries[]
             | select(.value.name=="tx_enable") | .key')
kubectl patch device w10-a --type=json -p \
  "[{\"op\":\"replace\",\"path\":\"/spec/properties/$i/desired/value\",\"value\":\"ON\"}]"
```

Then watch the two sides converge:

```bash
kubectl get devicestatus w10-a -o custom-columns=\
'NAME:.metadata.name,WANT:.status.twins[0].observedDesired.value,GOT:.status.twins[0].reported.value'
```
```
NAME    WANT   GOT
w10-a   ON     OFF     # a moment later...
w10-a   ON     ON
```

**The interesting bit:** that intermediate row. In Kubernetes, spec and status
diverge for milliseconds. Here it is visible, and on a battery node it will be
visible for hours. That gap is the thing your whole fleet controller reasons
about.

**What you added:** `DeviceDataWrite` in the driver.

---

## V2 — a fleet, and things that are not always reachable

**Goal:** many devices, some of them asleep, some behind a radio link.

EdgeCore moves to the ODROID-C2. Field nodes get their own Device objects even
though they have no IP and no WiFi — the mapper reaches them through the gateway
over LoRa.

```bash
kubectl get devices -o wide
```
```
NAME        MODEL          NODE        PROTOCOL   LAST SEEN   STATE
w10-gw      esphome-w10    odroid-c2   esphome    2s          Online
field-01    lora-sensor    odroid-c2   lora       4m          Online
field-02    lora-sensor    odroid-c2   lora       3h12m       Sleeping
field-03    lora-sensor    odroid-c2   lora       2d4h        Unreachable
```

`Sleeping` and `Unreachable` are the rows Kubernetes has no vocabulary for. A
pod in that state gets evicted. These must not be.

Find everything that has not converged:

```bash
kubectl get devices -o json | jq -r '
  .items[]
  | select(.spec.properties[]?.desired.value !=
           (.status.twins[]? | select(.propertyName == "tx_enable") | .reported.value))
  | .metadata.name'
```
```
field-02
field-03
```

That is your convergence report, and it is the primitive everything in V3 is
built on.

Queue intent for a node that is currently asleep:

```bash
kubectl patch device field-02 --type=merge -p \
  '{"spec":{"properties":[{"name":"sample_interval","desired":{"value":"300"}}]}}'

kubectl get device field-02 -o jsonpath='{.status.twins[0]}'
```
```json
{"propertyName":"sample_interval",
 "desired":{"value":"300"},
 "reported":{"value":"60","metadata":{"timestamp":"1756301000"}}}
```

Desired is set. Reported is stale. Nothing is wrong — the node simply has not
woken up yet. **This is the design question you have to answer:** if three more
patches land before it wakes, does it get all four in order, or only the last?

**What you added:** a LoRa driver, per-node addressing through the gateway, and
gateway firmware that caches last-known values and queues pending intent.

---

## V3 — rollouts

**Goal:** desired state for *firmware*, not just properties. Canary, health gate,
rollback.

This is where you stop using KubeEdge's CRDs and add your own.

```bash
kubectl apply -f - <<'EOF'
apiVersion: fleet.io/v1
kind: FirmwareRollout
metadata:
  name: sensors-v42
spec:
  selector:
    matchLabels:
      role: field-sensor
  source:
    configHash: a872123f
    package: oci://registry.local/w10-field:42
  strategy:
    canary: 1
    healthGate:
      mustReportWithin: 5m
    steps: [1, 10, 50, 100]
    rollbackOn:
      bootLoopThreshold: 2
EOF
```

```bash
kubectl get firmwarerollout sensors-v42
```
```
NAME          TARGET   UPDATED   HEALTHY   PHASE     STEP
sensors-v42   47       1         1         Canary    1/4
```

Watch it progress:

```bash
kubectl get firmwarerollout sensors-v42 -w
```
```
sensors-v42   47   1    1    Canary       1/4
sensors-v42   47   5    5    Progressing  2/4
sensors-v42   47   24   23   Progressing  3/4
sensors-v42   47   24   22   Paused       3/4   # a node failed its health gate
```

```bash
kubectl describe firmwarerollout sensors-v42
```
```
Events:
  Normal   CanaryHealthy   field-07 reported within 41s
  Normal   StepComplete    step 2/4, 5 devices updated
  Warning  HealthGateFail  field-19 did not report within 5m
  Warning  RolloutPaused   halting at step 3/4, 22/24 healthy
```

```bash
kubectl rollout undo firmwarerollout/sensors-v42
```

**What you added:** your own operator. Everything up to V2 was KubeEdge; this is
the layer that does not exist anywhere open source.

---

## The one-line summary of each

| | Command that defines it | What it proves |
|---|---|---|
| **V0** | `kubectl get device w10-a` shows a temperature | the chain works end to end |
| **V1** | `kubectl patch` makes an LED turn on | the loop closes |
| **V2** | `kubectl get devices` lists 40 things, some asleep | the model survives unreachability |
| **V3** | `kubectl get firmwarerollout` shows a canary | the fleet layer that nobody has built |

V0 and V1 are KubeEdge plus a driver you write. V2 is a second driver plus real
firmware work on the gateway. V3 is your own operator, and it is the part worth
funding.

**Do not start V1 until V0 prints a number.**
