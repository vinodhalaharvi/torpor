# torpor on an nRF9160 DK

The first device in this project that is not ESPHome, and the point of it is
to test whether `docs/device-contract.md` is really firmware-agnostic.

## What it does not have

**No SIM, so no network.** The nRF9160 is a cellular part — no BLE, no Thread —
and without a SIM it cannot reach a broker. So the contract travels over UART
and a host-side bridge republishes it to MQTT.

That bridge is a gateway in exactly the sense `w10-a` is a gateway for
`field-01`: a device with no address of its own, surfaced through something
that has one. Nothing in the mapper changes.

It is also the more useful shape. A serial bridge covers every serial-attached
device — Modbus RTU, RS-485 sensors, older industrial gear — which is a whole
class this project could not previously touch.

**No sensors.** The DK has no SHT41. Temperature and humidity are synthetic.
The contract is about the shape of what a device publishes; a real sensor
changes one function, not the protocol.

**No OTA.** Pulling firmware here means MCUmgr over SMP, which is real work.
Until it exists this device is monitorable and not updatable — `torpor verify`
reports 4 of 5, which is a state rather than a failure.

## Build

Secure mode, no `/ns`:

```bash
nrfutil toolchain-manager launch --shell
cd ~/ncs/v3.4.0/zephyr
west build -p -b nrf9160dk/nrf9160 \
  ~/embedded_projects/torpor/firmware/nrf9160 -d /tmp/torpor \
  -- -DTORPOR_ID=nrf-01 -DTORPOR_HASH=a1b2c3d
west flash -d /tmp/torpor
```

`/ns` builds three images — an immutable bootloader at 0x0, TF-M at 0x10000,
and the app at 0x30000 — and a `west flash --erase` removes the bootloader,
leaving a chip that verifies clean and boots into nothing. Secure mode is one
image at 0x0 with the whole 1 MB. Without a SIM there is nothing for TrustZone
to protect.

Diagnosing that took an hour. The tell is:

```
nrfutil device x-read --serial-number <sn> --address 0x00000000 --bytes 16
0x00000000: FFFFFFFF FFFFFFFF FFFFFFFF FFFFFFFF
```

An erased vector table. Worth running before anything else when a board is
flashed, verified, and silent.

## Line protocol

```
PUB <topic-suffix> <value>      device -> host
SUB <topic-suffix> <value>      host -> device
# anything                      diagnostics, ignored by the bridge
```

Not JSON, deliberately. Serial output gets corrupted by resets and line noise;
a malformed line here costs one reading, while a malformed JSON object costs a
parse error and a decision about what to do with it. It is also readable in
`tio` without a tool, which matters more during bring-up than elegance does.

## Run

```bash
make bridge DEVICE=nrf-01 PORT=/dev/tty.usbmodem0009600198281
torpor verify --device nrf-01 --broker tcp://192.168.68.113:1883
```
