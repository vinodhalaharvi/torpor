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
