# What a device has to do

Everything above the mapper is firmware-agnostic. This file is the contract a
device implements to join a torpor fleet, whatever it runs.

The reference implementation is ESPHome, because that is what was on the desk.
Nothing above the mapper knows ESPHome exists, and this document exists so that
"what would it take to use Zephyr" has an answer shorter than "read the source".

---

## The whole contract

Five things. Four of them are publishing a string.

| # | The device must | Why |
|---|---|---|
| 1 | publish sensor values | the point |
| 2 | publish what firmware it is **running** | the health gate compares against this |
| 3 | publish a retained birth, and set a will | liveness distinguishes gone from quiet |
| 4 | publish a retained announcement on boot | enrollment, so nobody transcribes fifteen manifests |
| 5 | accept a firmware URL and **pull** from it | the inversion that makes fleet updates possible |

That is all. A device doing those five things is indistinguishable from a W10
to everything above the mapper.

---

## 1–2. Sensor values, and the running hash

```
<prefix>/sensor/temperature/state           26.8
<prefix>/text_sensor/running_config_hash/state   43fdb47c
```

Topics are per-device configuration, not code. The paths above are ESPHome's
convention and the manifests point at them; a Zephyr device publishing
`nrf/field-01/temp` works identically with a different `visitors.topic`.

**The running hash is the one that is not negotiable, and it must describe the
running firmware rather than echo an instruction.**

A device that reports the hash it was *told* proves nothing. That failure
happened here, on hardware: a tone that never played while the twin converged,
the broker round-tripped cleanly and `kubectl` was happy. Every observable
signal said success.

In ESPHome the value is a build-time substitution, so it cannot be anything
other than what is running. In Zephyr, MCUboot's image header hash serves the
same purpose and is better — it is computed from the image, not asserted about
it.

## 3. Birth and will

```
<prefix>/status   online     retained
<prefix>/status   offline    retained, as the MQTT will
```

The retained will is what separates *gone* from *quiet*. Without it, a device
that has not spoken for six hours is ambiguous; with it, the broker says
whether the connection died or the device simply had nothing to say.

For a gateway-relayed device there is no birth or will — nothing on the far end
can announce itself and nothing notices when it stops. Liveness falls back to
inferring state from silence measured against that device's own expected
interval, which is why `expectedIntervalSeconds` is per-device.

## 4. Announcement

```
torpor/announce/<device>   retained
```

```json
{"device":"field-01","model":"heltec-v3","gateway":"gw-north","nodeID":2,
 "topicPrefix":"field-01","configHash":"v41",
 "firmwareVersion":"...","buildTime":"2026-08-30T16:12:00Z"}
```

Retained, so a device that announced before the controller started is not lost.

**Every field is a claim.** Anyone who can reach the broker can publish this,
which is why enrollment requires approval. See `DeviceEnrollment`.

`buildTime` is worth carrying even though nothing requires it: a device
announcing a build older than the current firmware was flashed from a stale
cache, which happened here and cost an hour.

## 5. Firmware: the device pulls

```
<prefix>/text/firmware_url/command    "<hash>|<url>"
```

The device fetches the image itself. **This inversion is the load-bearing part
of the contract.**

A push requires the controller to reach every device simultaneously, and half
of them are asleep. A pull means the instruction is a few bytes of desired
state that can sit queued for a node which is not currently listening — which
is what makes a fleet update possible at all, rather than merely slow.

Two behaviours the device owns, both learned the hard way:

**Deduplicate.** The mapper writes desired state on every collect cycle, not
on change. Without a guard the device re-flashes every ten seconds. Keep the
last token and return early if it repeats.

**Refuse a no-op.** If the requested hash matches what is already running, do
nothing. Re-flashing a converged device is pure risk.

```c
static char last[64];
if (!strcmp(tok, last)) return;
strcpy(last, tok);
if (!strcmp(want_hash, RUNNING_HASH)) return;   // already there
ota_pull(url);
```

---

## Optional, and what each unlocks

| Publish | Unlocks |
|---|---|
| `sensor/battery_percent/state` | `OTAExceedsBatteryBudget`, `needs-site-visit` |
| `sensor/boot_count/state` | boot-loop detection, `rollbackOn.bootLoopThreshold` |
| `text_sensor/cert_expires_at/state` | `CredentialExpiry`, the `AtRisk` column |
| `<gateway>/lora/rx` frames | relayed devices with no address of their own |

None is required. Each one turns a column from `Unknown` into an answer.

---

## Porting to Zephyr / nRF

| Contract item | Zephyr |
|---|---|
| MQTT publish/subscribe | `zephyr/net/mqtt.h` |
| Running hash | MCUboot image header — computed from the image, not asserted |
| Birth / will | MQTT connect + LWT, same as anywhere |
| Announcement | one publish on connect |
| Pull-based OTA | **MCUmgr / SMP over the transport** |

The first four are an afternoon. The fifth is the real work — and it is also
where the *interesting* nRF capability lives, because SMP over BLE or Thread is
the path DFU actually takes on those platforms, and neither has an ESPHome
equivalent today.

Worth noting what does not change: the mapper. Transport selection, staleness
decay, and refusal are not ESPHome-specific. A Zephyr mapper reuses most of
`driver.go` and swaps the topic layout.

## Porting to anything else

The same five items apply to a Modbus device behind KubeEdge's existing mapper,
with registers instead of topics. `running_config_hash` becomes a firmware
version register; the pull becomes a vendor OTA path or, more likely,
`ota: false` and an honest refusal.

**A device that cannot report what it is running cannot be managed by this
system**, and that is the honest boundary. Everything else degrades gracefully
into `Unknown` and `Refused`, which are answers. That one is not.
