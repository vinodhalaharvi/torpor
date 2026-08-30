# Protocol capability matrix

What each transport can carry, and which version of the fleet controller needs it.

---

## The master table

| Transport | Throughput (real) | 1 MB firmware | Addressable? | Telemetry | Config push | Firmware OTA | ESPHome support | Idle current | Version |
|---|---|---|---|---|---|---|---|---|---|
| **WiFi** | 10+ Mbps | seconds | IP | ✅ | ✅ | ✅ | native, built in | 80–100 mA | **V0** |
| **Ethernet** | 100 Mbps | seconds | IP | ✅ | ✅ | ✅ | native | n/a (mains) | **V1** |
| **Thread** | 20–50 kbps | 3–7 min | **IPv6** | ✅ | ✅ | ✅ * | `openthread:`, C6/H2 only | ~1 mA avg | **V2** |
| **BLE** | 100–700 kbps | 30–90 s | no IP | ✅ | ✅ | ❌ ** | server/client, no OTA | ~1 mA avg | **V2** |
| **LoRa** | ~1.7 kbps @ SF9 | **hours — don't** | no IP | ✅ | ✅ | ❌ | `sx126x:` | µA sleeping | **V2** |
| **Zigbee** | ~20 kbps effective | 10–30 min | no IP | ✅ | ✅ | ❌ ** | `zigbee:`, C5/C6/H2 | ~1 mA avg | V3+ |
| **Cellular** | Mbps | seconds | IP | ✅ | ✅ | ✅ | partial | high | V3+ |

\* Thread OTA works, but a sleepy end device must set `poll_period: 0` for the
duration, then restore it. It stops being sleepy while updating.

\*\* Technically capable — Nordic DFU over BLE is how every fitness tracker
updates — but **no ESPHome component exists**. Unavailable without writing one.

---

## The two properties that actually matter

Everything above collapses to two questions per device:

**Is it addressable?** WiFi, Ethernet and Thread give you an IP. The mapper can
open a connection. BLE, LoRa and Zigbee do not — those devices are reachable
only *through* something else, and the Device object points at a gateway.

**Can it carry a megabyte?** WiFi and Ethernet trivially. Thread slowly but
acceptably. LoRa never.

```
addressable + fast     ->  normal OTA, poll or subscribe          WiFi, Ethernet
addressable + slow     ->  OTA with a wake window                 Thread
not addressable        ->  gateway holds state, config-only       LoRa, BLE, Zigbee
```

---

## Which version needs what

### V0 — WiFi only

Both W10s on USB, WiFi up, `api:` listening on 6053. One driver. Everything
addressable and awake. The point is to prove the chain, so nothing here should
be interesting.

### V1 — WiFi, plus Ethernet for the edge node

EdgeCore moves to the ODROID-C2 over Ethernet. Devices are still WiFi. The write
path lands. Still nothing unreachable.

### V2 — LoRa and Thread. This is where it gets real.

The first version where a device may be **not addressable** or **asleep**. Three
new states the model has to carry:

```
Online       reachable now
Sleeping     known-good, will check in on its own schedule
Unreachable  has not checked in for longer than expected
```

Kubernetes evicts a node in the last two states. You must not.

And the first version where a device can be `config: true, ota: false` — the
capability split that no commercial platform models, because they all assume IP.

### V3 — rollouts across mixed transports

The controller has to plan around capability rather than treating the fleet as
uniform:

```yaml
spec:
  transports:
    - type: lora        # always available
      config: true
      ota: false
    - type: wifi        # only when the node is in range
      config: true
      ota: true
```

A rollout targeting a `lora`-only node should be **refused**, not attempted and
timed out. A node that is occasionally WiFi-reachable becomes *pending until next
in range* — a legitimate long-lived state, not a failure.

---

## Where each of your boards sits

| Board | Transports | Addressable | OTA | Role |
|---|---|---|---|---|
| **W10 ×2** | WiFi + LoRa + BLE | yes, via WiFi | yes | V0/V1 device; later the **gateway** |
| **ESP32-C6** | WiFi + Thread + BLE + Zigbee | yes | yes | V2 Thread node — the most capable radio you own |
| **ESP32-S3** | WiFi + BLE | yes | yes | V0 spare |
| **XIAO S3** | WiFi + BLE | yes | yes | camera node |
| **ODROID-C2** | Ethernet | yes | n/a | **edge node** — runs EdgeCore + mapper |

The C6 is the piece worth remembering. It is the only board you have with an
802.15.4 radio, which makes it your Thread test node — the one transport that is
both low-power *and* firmware-updatable, and therefore the most interesting case
in V2.

---

## The one-sentence version

**WiFi for anything that can afford 100 mA. Thread for low-power devices you
still need to update. LoRa for range, and accept that those nodes get config
patches and an annual visit.**
