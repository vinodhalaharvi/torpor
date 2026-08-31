package internal

import (
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// transportDefaults is docs/protocol-matrix.md, executable.
//
// The matrix was written before any of this existed, from datasheets and
// airtime arithmetic. Encoding it here rather than leaving it in prose means
// the planner refuses a rollout for the same reason a human would, and that
// the reason is auditable.
//
// These are DEFAULTS. A Device can override any of them, because a real
// deployment knows things a table cannot — a Thread node with a mains-powered
// parent, a LoRa link at SF7 rather than SF9, a gateway with a better antenna.
// The table is what to assume when nobody has said otherwise.
var transportDefaults = map[string]fleetv1.Transport{

	// Addressable and fast. The uninteresting case, which is why every
	// commercial platform assumes it.
	"wifi": {
		Type:            "wifi",
		Availability:    "always",
		Config:          true,
		OTA:             true,
		ThroughputBps:   10_000_000,
		MaxPayloadBytes: 16 << 20,
		OTACostMah:      2, // seconds of transfer, but ~90 mA to stay associated
	},

	"ethernet": {
		Type:            "ethernet",
		Availability:    "always",
		Config:          true,
		OTA:             true,
		ThroughputBps:   100_000_000,
		MaxPayloadBytes: 64 << 20,
	},

	// The interesting one. IPv6-addressable AND low-power — the only transport
	// that is both, which makes it the only place the battery-cost refusal has
	// anything to bite on.
	//
	// OTA works, but a sleepy end device must set poll_period: 0 for the
	// duration and restore it after. It stops being sleepy while updating,
	// which is why OTACostMah is an order of magnitude above WiFi's despite
	// the transfer being slower rather than more power-hungry per byte.
	"thread": {
		Type:            "thread",
		Availability:    "always",
		Config:          true,
		OTA:             true,
		ThroughputBps:   50_000,
		MaxPayloadBytes: 1 << 20,
		OTACostMah:      34,
	},

	// config: true, ota: false — and the false is permanent.
	//
	// Not "OTA is unimplemented for LoRa". A 1 MB image at ~1.7 kbps is
	// roughly 80 minutes of continuous airtime, inside a 1% duty cycle that
	// permits about 36 seconds per hour. That is not slow, it is arithmetic
	// that never terminates. A rollout targeting a LoRa-only node should be
	// refused rather than attempted, and this is the line that does it.
	"lora": {
		Type:             "lora",
		Availability:     "always",
		Config:           true,
		OTA:              false,
		ThroughputBps:    1_760,
		MaxPayloadBytes:  240,
		DutyCyclePercent: "1.0",
	},

	// Technically capable — Nordic DFU over BLE is how every fitness tracker
	// updates — but no ESPHome component exists. Capable in principle,
	// unavailable in practice, and only this model records the difference.
	"ble": {
		Type:            "ble",
		Availability:    "opportunistic",
		Config:          true,
		OTA:             false,
		ThroughputBps:   700_000,
		MaxPayloadBytes: 512,
	},

	// Same shape as BLE: the standard supports OTA, the toolchain does not.
	"zigbee": {
		Type:            "zigbee",
		Availability:    "always",
		Config:          true,
		OTA:             false,
		ThroughputBps:   20_000,
		MaxPayloadBytes: 82,
	},

	// A shared bus with one master. Two hundred devices on a segment cannot be
	// polled concurrently — the bus serialises and a slow device delays
	// everyone behind it. Structurally the same problem as LoRa duty cycle:
	// a scarce shared medium the planner must schedule around rather than
	// assume away.
	"modbus": {
		Type:            "modbus",
		Availability:    "always",
		Config:          true,
		OTA:             false,
		ThroughputBps:   115_200,
		MaxPayloadBytes: 252,
	},

	"cellular": {
		Type:            "cellular",
		Availability:    "always",
		Config:          true,
		OTA:             true,
		ThroughputBps:   1_000_000,
		MaxPayloadBytes: 16 << 20,
		OTACostMah:      120,
	},
}

// defaultTransport returns the assumed capability for a transport name.
// Unknown transports get config-only, which is the conservative answer: a
// planner that guesses "probably OTA-capable" and is wrong bricks nothing, but
// it does produce a rollout that hangs instead of one that refuses.
func defaultTransport(name string) fleetv1.Transport {
	if t, ok := transportDefaults[name]; ok {
		return t
	}
	return fleetv1.Transport{
		Type:         name,
		Availability: "always",
		Config:       true,
		OTA:          false,
	}
}
