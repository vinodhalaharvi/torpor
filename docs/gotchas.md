# Gotchas

Things that cost real time, none of them documented upstream. Companion to the
hardware findings in the handoff doc.

---

## KubeEdge 1.23

**Twins are not on the Device.** v1beta1 extracted device status into a
separate `DeviceStatus` CRD. The reported value lives at
`devicestatus/<name>.status.twins[].reported.value`, and the desired side is
`observedDesired`, not `desired`. `device/<name>.status.twins[]` still exists
and is always empty, so a jsonpath against it returns nothing with no error.

**CloudCore has no RBAC for that CRD.** The 1.23.1 Helm chart's ClusterRole
was never updated. CloudCore installs, reports Running, and fails every status
reconcile with `devicestatuses.devices.kubeedge.io is forbidden`. No
DeviceStatus object is ever created, so the device looks healthy and reports
nothing. See `manifests/cloudcore-devicestatus-rbac.yaml`, applied by
`make rbac`.

**`collectCycle` and `reportCycle` are milliseconds.** They look like the
nanosecond durations used elsewhere in Kubernetes. `mapper-framework` does
`time.Millisecond * time.Duration(twin.Property.CollectCycle)`, so a value of
`10000000000` schedules the first collection about 115 days out. Nothing
errors — the ticker is simply set past the end of the project.

**Mapper registration is in-memory.** Restarting edgecore loses it while the
device stays cached in SQLite, so neither side pushes and the mapper sits idle
forever. Whichever process restarts last catches up: restart the mapper after
edgecore, never before.

**`eventbus` wants a broker inside the edge node.** It is the pre-DMI device
path and retries `127.0.0.1:1883` every five seconds forever, burying every
other log line. Disable it in `/etc/kubeedge/config/edgecore.yaml` when using
a DMI mapper.

**A NotReady edge node is fine.** Readiness comes from `edged`, which needs
CNI. The device path — edgehub, metamanager, devicetwin, DMI — does not touch
it. Devices report normally on a node Kubernetes calls NotReady, which is this
project's own thesis arriving a version early.

---

## kubectl

**Never merge-patch `spec.properties`.** It is a list, and a merge patch
replaces a list rather than merging by name:

```bash
# WRONG — deletes every other property and strips this one's visitors
kubectl patch device w10-a --type=merge -p \
  '{"spec":{"properties":[{"name":"amp_enable","desired":{"value":"ON"}}]}}'
```

The mapper then receives a property with `visitors:{}` and panics in
`buildPropertiesFromGrpc` on the nil visitor config. Use a JSON patch against
the element's index instead — `make set PROPERTY=... VALUE=...` does this.

**`kubectl` exits 0 for a jsonpath that matches nothing.** A check that only
tests the exit code will report success next to a blank line. Capture the
value and test it for emptiness.

---

## ESPHome MQTT

**Not every platform honours an explicit `command_topic`.**

- `text` **rejects** it at config-validation time. Clear failure, easy to fix.
- `number` **accepts it and silently ignores it.** The boot log is the tell:
  every writable entity prints both topics, and the number printed only a
  State Topic line. The board was never subscribed to the topic the mapper
  published on, so commands vanished with no error anywhere in the stack.

When an entity needs a write path, let ESPHome derive both topics and point
the Device manifest at whatever it derives. Read them off the boot log:

```
[C][mqtt.switch:043]:   State Topic:   'w10-a/switch/speaker_amp/state'
[C][mqtt.switch:045]:   Command Topic: 'w10-a/switch/speaker_amp/command'
```

**Use `text`, not `number`, for trigger properties.** A trigger is a token
being echoed, not a quantity. `number` publishes `0.000000` rather than `0`,
is float32 internally so anything past seven significant digits is unreliable
— a unix timestamp is ten — and does not take a command topic. `text`
publishes the string verbatim.

**The SX1262 on the W10 identifies as an SX1261.** The boot log reports
`HW Version: SX1261 V2D 2D02` while the vendor documentation and the config
both say 1262. It works, but the 1261 tops out around +15 dBm against the
1262's +22, so a config asking for `pa_power: 22` is not getting it. First
thing to check if LoRa range disappoints.

**`optimistic: true` skips `on_value` for commanded values.** On a template
`text`, optimistic mode publishes the commanded value straight back to the
state topic without running the automation. The twin converges perfectly and
nothing happens — the worst possible failure mode, because every signal says
success.

Setting the same entity from the web UI *does* run `on_value`, which is what
isolates it: if the web UI works and MQTT does not, suspect optimistic mode.

Use `set_action` instead. It is the hook for externally-commanded values, and
publishing the state as its first step keeps the echo that convergence depends
on:

```yaml
set_action:
  - text.template.publish:
      id: tone_trigger
      state: !lambda 'return x;'
  - # ...the actions that should actually run
```

---

## Trigger properties

**The mapper writes desired state on every collect cycle, not on change.**
`set_action` therefore runs on every write, and an actuator driven from a
desired value fires repeatedly forever — the same token replayed the tone every
ten seconds until the device started deduplicating.

Idempotence has to live **at the device**. Nothing upstream guarantees a single
write:

```yaml
- lambda: |-
    static std::string last_token = "";
    id(trigger).publish_state(x);
    if (x.empty() || x == last_token) return;
    last_token = x;
    // ...the action
```

This matters far more for V3 than for a tone. A rollout property that
re-triggers a flash on every reconcile would be a very bad afternoon.

**Do not put a device-local transient under fleet reconciliation.** The amp is
switched on by the tone action and off again afterwards. It was also a Device
property with `desired: "OFF"`, so the mapper wrote `OFF` on its own cycle and
switched the amp off 100 ms into a 2.5 second tone — everything working exactly
as designed, fighting itself.

Anything the device manipulates transiently during an operation should not
simultaneously be declared desired state, or the controller will stomp on it
mid-operation.

**Two rules for health gates, both learned the hard way:**

- *Convergence is not proof of effect.* `optimistic: true` echoed the commanded
  value back with the automation never running. Twin converged, broker showed a
  clean round trip, `kubectl` was happy, hardware did nothing.
- *A converged property may still be actively driving the device.* Desired
  equalled reported the whole time the tone was looping.

Whatever a rollout gates on has to be a consequence of the change, not an echo
of the request.

**ESPHome will flash a stale binary without saying so.** `esphome run` reuses a
cached build when it thinks nothing changed, and reports `OTA successful` for
firmware you did not write. The only tell is the compile timestamp in the boot
banner:

```
[I][app:151]: ESPHome version 2026.8.1 compiled on 2026-08-30 15:47:48
```

If that predates your last `git pull`, you flashed the old thing. `make
clean-fw` clears both caches. Check the timestamp after every flash that is
supposed to change behaviour — it costs a second and it cost an hour once.

**A generated file is only correct until the source changes.** `w10-msg-b.yaml`
was generated from A, then A gained the LoRa decoder and the 30s broadcast and
B did not. B flashed cleanly, ran, and quietly lacked half the feature — which
looked exactly like a radio problem, and cost several rounds of chasing one.

`make flash-b` now depends on `make gen-b`, so regenerating is not a thing
anyone has to remember. If a check can be a dependency, make it a dependency.
