# One fleet, eight objects

Every CRD in this project, told as one story: forty-seven sensors at a water
utility, spread over three pump houses, most of them on LoRa and a few within
WiFi range of a gateway.

Read top to bottom. Each object exists because the one before it left a
question unanswered.

---

## 1. DeviceTemplate — the fleet exists

Forty-seven devices that are the same except where they are not.

```yaml
apiVersion: fleet.torpor.io/v1alpha1
kind: DeviceTemplate
metadata:
  name: north-ridge-sensors
spec:
  deviceModelRef: lora-sensor

  # Every generated Device gets these. This is what makes every query and
  # rollout below able to say anything at all — a selector over
  # `role: field-sensor` means something only because something stamped that
  # label on forty-seven devices consistently.
  labels:
    role: field-sensor
    site: north-ridge

  protocol:
    name: esphome-mqtt
    configData:
      broker: tcp://10.0.4.2:1883
      gateway: "{{ .gateway }}"
      nodeID: "{{ .nodeID }}"
      expectedIntervalSeconds: "1800"     # every 30 minutes, on battery

  properties:
    - name: temperature
      collectCycle: 60000
      reportToCloud: true
      visitors: {dataType: float, topic: temperature}

    - name: sample_interval
      desired: "1800"                     # fleet-wide default
      visitors:
        dataType: string
        topic: text/sample_interval/state
        commandTopic: text/sample_interval/command

  instances:
    - name: field-01
      vars: {gateway: gw-north, nodeID: "2"}
      labels: {pumphouse: north}

    - name: field-02
      vars: {gateway: gw-north, nodeID: "3"}
      labels: {pumphouse: north}
      # A regulator watches this one, so it samples six times as often.
      # An exception expressed as an exception, rather than as a second
      # template nobody remembers to keep in sync.
      overrides:
        sample_interval: "300"

    - name: field-19
      vars: {gateway: gw-south, nodeID: "20"}
      labels: {pumphouse: south}
```

`prune` defaults false. A device disappearing from this list is far more likely
an editing mistake than a decommissioning, and the costs are not symmetrical.

---

## 2. Device — one instance, two doors

What the template generates, plus what makes this project different: a device
declaring more than one way in.

```yaml
apiVersion: devices.kubeedge.io/v1beta1
kind: Device
metadata:
  name: field-19
  labels: {role: field-sensor, site: north-ridge, pumphouse: south}
spec:
  deviceModelRef: {name: lora-sensor}
  nodeName: edge-01

  protocol:
    protocolName: esphome-mqtt
    configData:
      broker: tcp://10.0.4.2:1883

      transports:
        - type: lora
          gateway: gw-south
          nodeID: 20
          config: true
          ota: false            # 240 byte frames. Arithmetic, not a gap.
          maxWriteBytes: 240
          staleAfterSeconds: 5400

        - type: wifi
          topicPrefix: field-19
          config: true
          ota: true             # the only door firmware fits through
          staleAfterSeconds: 600

      expectedIntervalSeconds: 1800
```

Note what is absent from the LoRa transport: an address. There is a gateway and
a one-byte node id, and that is the entire scheme.

---

## 3. DeviceLiveness — derived, never written

Nobody types this. It accumulates.

```yaml
apiVersion: fleet.torpor.io/v1alpha1
kind: DeviceLiveness
metadata: {name: field-19}
status:
  state: Sleeping
  transport: lora
  gateway: gw-south
  silentFor: 22m
  nextExpectedBy: "2026-08-31T04:30:00Z"
  assessment: WithinExpectedWakeInterval

  capability:
    transports:
      - {type: lora, availability: always,        config: true, ota: false}
      - {type: wifi, availability: opportunistic, config: true, ota: true, otaCostMah: 34}
    reachableVia: [lora]          # observed, and it decays
    batteryPercent: 61
    runningConfigHash: "a41f09c"

  contactWindows:
    - transport: wifi
      typicalInterval: 24h
      typicalDuration: 4m
      confidence: Medium
      samplesObserved: 9
```

```
$ kubectl get liveness

NAME       TRANSPORT  STATE      SILENT  ASSESSMENT
gw-north   wifi       Online     3s      ReportingOnSchedule
field-01   lora       Sleeping   14m     WithinExpectedWakeInterval
field-19   lora       Sleeping   22m     WithinExpectedWakeInterval
field-33   lora       Unreachable 4d2h   NoReportFor4d2h
```

`Sleeping` is healthy. A Node in that condition would be `NotReady` and its
pods evicted.

---

## 4. MaintenanceWindow — policy, written by somebody else

```yaml
apiVersion: fleet.torpor.io/v1alpha1
kind: MaintenanceWindow
metadata: {name: north-ridge-nightly}
spec:
  selector:
    matchLabels: {site: north-ridge}
  timezone: America/New_York

  allow:
    - cron: "0 2 * * *"
      duration: 3h
      reason: nightly maintenance

  deny:
    - from: "2026-12-20T00:00:00Z"
      until: "2027-01-05T00:00:00Z"
      reason: holiday change freeze

  # A window is about when to START. Aborting a transfer at the boundary
  # leaves a half-written device, and a half-written device is a brick.
  allowInFlightToComplete: true

  # Blast radius, not throughput. If the image is bad, this is how many
  # devices break before anybody has a chance to notice.
  maxDevicesPerWindow: 10
```

This object does not know which rollouts it governs. It selects devices; the
rollout selects devices; they meet on the labels the template stamped.

---

## 5. FirmwareRollout — and the column nobody else has

```yaml
apiVersion: fleet.torpor.io/v1alpha1
kind: FirmwareRollout
metadata: {name: sensors-v42}
spec:
  selector:
    matchLabels: {role: field-sensor}

  source:
    package: oci://registry.local/field-sensor:42
    configHash: "b7d2e10"
    sizeBytes: 1163019

  # Checked BEFORE anything is transmitted.
  requires:
    ota: true
    minBatteryPercent: 25
    maxSilentFor: 48h

  strategy:
    canary: 1
    steps: [1, 10, 50, 100]     # cumulative, of ELIGIBLE
    maxConcurrent: 1
    healthGate:
      mustReportWithin: 30m
      mustReportConfigHash: true    # what it is RUNNING, not what it was told
      settleFor: 2h                 # a device that boots then boot-loops
    rollbackOn:
      bootLoopThreshold: 2
      healthGateFailures: 1
```

```
$ kubectl get firmwarerollout sensors-v42

NAME          TARGET  ELIGIBLE  UPDATED  HEALTHY  PHASE    STEP
sensors-v42   47      12        1        1        Canary   1/4
```

```yaml
status:
  refused:                        # permanent. stop asking.
    - device: field-01
      reason: NoOTACapableTransport
      detail: "transports: lora — none can carry firmware"
    - device: field-41
      reason: OTAExceedsBatteryBudget
      detail: "34 mAh of 210 mAh remaining (16%)"

  pending:                        # temporary. ask again later.
    - device: field-19
      reason: AwaitingTransportWindow
      detail: "reachable via [lora], none OTA-capable"
    - device: field-07
      reason: DeviceSleeping
      detail: "silent 22m, next expected by 2026-08-31T04:30:00Z"
```

47 targeted, 12 eligible. Thirty-five devices are not failing — most of them
can never take this at all, and knowing that before transmitting is the point.

`field-19` is the interesting row: **Online, fully OTA-capable, and still
pending** — because the door that is open right now cannot carry firmware.

### Blocked, which is not Waiting

```yaml
status:
  phase: Blocked
  blockedBy: north-ridge-nightly
  blockReason: holiday change freeze
  nextWindow: "2027-01-05T02:00:00Z"
```

`Waiting` means the devices are unavailable. `Blocked` means they are available
and we have been told not to touch them. Reporting a change freeze as "waiting
for devices" sends somebody to debug a radio.

---

## 6. FleetDrift — did it stay true?

A rollout says a change succeeded once. This says whether it is still true in
March.

```yaml
apiVersion: fleet.torpor.io/v1alpha1
kind: FleetDrift
metadata: {name: field-sensor-drift}
spec:
  selector:
    matchLabels: {role: field-sensor}
  expect:
    configHash: "b7d2e10"
  gracePeriod: 4h        # a device mid-rollout disagrees legitimately
  ignoreSleeping: false  # a sleeping device that was wrong is still wrong
```

```
$ kubectl get fleetdrift

NAME                 TOTAL  CONVERGED  DRIFTED  UNKNOWN  OLDEST
field-sensor-drift   47     38         7        2        94d3h
```

```yaml
status:
  devices:
    - device: field-22
      expected: "b7d2e10"
      actual: "9c1a880"
      driftAge: 94d3h
      remediable: true
      assessment: DriftedAndReachable
    - device: field-01
      driftAge: 61d
      remediable: false
      assessment: DriftedNoCapableTransport
```

`oldestDrift: 94d3h` is the number worth watching. A fleet 15% drifted for an
hour is mid-rollout; 15% drifted for three months is a process problem, and the
percentage looks identical.

---

## 7. FleetQuery — selecting on what labels cannot hold

```yaml
apiVersion: fleet.torpor.io/v1alpha1
kind: FleetQuery
metadata: {name: drifted-and-fixable}
spec:
  where:
    runningConfigHash: {notIn: ["b7d2e10"]}
    otaReachable: true
```

```
$ kubectl get fleetquery

NAME                  MATCHED  ACTIONABLE  NEEDS-SOMETHING-ELSE
drifted-total         7        1           6
drifted-and-fixable   1        1           0
needs-site-visit      3        0           3
awaiting-wifi-window  4        0           4
```

Same drift number, four different answers. One is a rollout, one is waiting,
one is a truck.

Three more worth having:

```yaml
# A truck schedule: low battery AND no way to help remotely.
spec:
  where:
    batteryBelowPercent: 20
    otaCapable: false
---
# Quiet for longer than THEIR OWN expectation. Not a wall-clock threshold —
# a 30-minute node silent for 6 hours is gone; a daily node is fine.
spec:
  where:
    silentBeyondExpected: true
---
# The transport ladder made visible: declared WiFi, not currently on WiFi.
# Not broken, not asleep. Waiting for the truck to drive past.
spec:
  where:
    transport: {equals: wifi}
    reachableVia: {notIn: ["wifi"]}
```

---

## 8. CredentialExpiry — a rollout with a deadline

A firmware rollout that fails leaves a device on old firmware. A credential
rotation that fails leaves it **permanently unreachable**, because the thing
you would use to fix it is the thing that expired.

```yaml
apiVersion: fleet.torpor.io/v1alpha1
kind: CredentialExpiry
metadata: {name: field-sensor-certs}
spec:
  selector:
    matchLabels: {role: field-sensor}
  expiryProperty: cert_expires_at
  warnBefore: 90d              # long, because the output is a work order
  rotationSizeBytes: 2048
  requiredContactWindow: 2m
```

```
$ kubectl get credentialexpiry

NAME                 TOTAL  EXPIRING  AT-RISK  EXPIRED  NEXT-EXPIRY
field-sensor-certs   47     12        31       0        2026-10-30
```

```yaml
status:
  devices:
    - device: gw-north
      state: Expiring
      timeLeft: 60d0h
      rotatableVia: [wifi]
      actionRequired: "rotate before 2026-10-30"

    - device: field-01
      state: AtRisk
      timeLeft: 60d0h
      rotatableVia: []
      reason: "no transport can carry a 2048 byte credential"
      actionRequired: "schedule site visit before 2026-10-30"
```

**Same expiry date, completely different response.** One is a config push, the
other is somebody driving out there. No certificate manager can compute the
`AtRisk` column, because it requires knowing what a device can *receive* rather
than whether it is online — and knowing it ninety days out rather than on the
morning the device goes dark.

---

## 9. DeviceDecommission — leaving without pretending you never joined

```yaml
apiVersion: fleet.torpor.io/v1alpha1
kind: DeviceDecommission
metadata: {name: field-22-battery}
spec:
  deviceRef: field-22
  reason: BatteryExpired
  detail: "cell at 2.1V, site visit 2026-08-14, not worth replacing at this site"
  effectiveFrom: "2026-08-14T10:00:00Z"   # paperwork lags the event
  replacement: field-22b
  deleteDeviceObject: false               # the point of the object
```

```yaml
status:
  phase: Complete
  serviceLife: 1459d23h
  lastSeenAlive: "2026-08-14T09:41:00Z"
  finalConfigHash: "9c1a880"
  credentialsRevoked: true
  excludedFromQueries: true
```

`serviceLife: 1459d23h` — four years — is the question anybody procuring the
next batch will ask, and it exists nowhere after a `kubectl delete`.

`finalConfigHash` is occasionally the most important field in an incident.
Three devices dying within a week of each other on the same firmware is a
pattern that is invisible once the objects are gone.

```
$ make attrition

REASON             COUNT
Failed             9
Stolen             4
BatteryExpired     11
```

A procurement question, not an ops one.

---

## The chain, in one place

```
DeviceTemplate     stamps labels     -> every selector below works
Device             declares doors    -> capability is knowable, not discovered
DeviceLiveness     watches           -> Sleeping is not Unreachable
MaintenanceWindow  permits           -> Blocked is not Waiting
FirmwareRollout    plans             -> Refused is not Failed
FleetDrift         remembers         -> "still true?" not "did it work?"
FleetQuery         asks              -> matched is not actionable
CredentialExpiry   warns             -> AtRisk is not Expiring
DeviceDecommission forgets, properly -> excluded is not deleted
```

Every line is the same shape: a distinction other tools collapse, and a
consequence of collapsing it.

## Status

V0–V2 run on hardware. The nine objects above compile and are unit-tested;
none has reconciled a live object yet. See the status table in the README.
