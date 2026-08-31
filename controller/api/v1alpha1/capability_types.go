package v1alpha1

// Transport is what a device can do over one radio or wire, right now.
//
// This is the field nothing else has. Every commercial platform stores one
// capability per device, because they assume IP and IP is uniform. A node that
// is LoRa-always and WiFi-occasionally has two different answers to "can you
// take a firmware image", and which one applies depends on where the node is
// standing this afternoon.
//
// Declared on the Device rather than inferred, because inference requires
// trying — and trying is what this model exists to avoid.
type Transport struct {
	// Type: lora, wifi, thread, ble, zigbee, ethernet, modbus, cellular.
	Type string `json:"type"`

	// Availability:
	//
	//   always         the transport is up whenever the device is
	//   opportunistic  up when the device happens to be in range. Not a
	//                  failure mode — a legitimate steady state for a node
	//                  that meets WiFi when a truck drives past
	//   scheduled      up during a known window
	// +optional
	Availability string `json:"availability,omitempty"`

	// Config: can carry a property write. Nearly always true — a few bytes
	// fit through almost anything.
	// +optional
	Config bool `json:"config,omitempty"`

	// OTA: can carry a firmware image.
	//
	// The split between this and Config is the whole capability model. A LoRa
	// node at 1.7 kbps is config: true, ota: false — not because OTA is
	// unimplemented but because a megabyte would take days of continuous
	// airtime and the duty cycle forbids it. Permanently false, and a rollout
	// should say so rather than time out.
	// +optional
	OTA bool `json:"ota,omitempty"`

	// MaxPayloadBytes is the largest artifact this transport can carry in a
	// reasonable window. Lets a controller refuse on size rather than on a
	// blanket ota: false — 200 KB over Thread is fine, 2 MB is not.
	// +optional
	MaxPayloadBytes int64 `json:"maxPayloadBytes,omitempty"`

	// ThroughputBps, for estimating transfer time before committing to it.
	// +optional
	ThroughputBps int64 `json:"throughputBps,omitempty"`

	// DutyCyclePercent is a regulatory ceiling, not a preference. 1% at
	// 915 MHz means a gateway has about 36 seconds of airtime per hour to
	// share across every node behind it. A planner that ignores this produces
	// schedules that are illegal rather than merely slow.
	// +optional
	DutyCyclePercent string `json:"dutyCyclePercent,omitempty"`

	// OTACostMah is what one firmware transfer costs this device in battery.
	//
	// On a sleepy Thread node the transfer is not the expensive part — leaving
	// sleep to receive it is. A rollout that refuses on energy grounds is
	// something no platform models, and this is the number it refuses against.
	// +optional
	OTACostMah int32 `json:"otaCostMah,omitempty"`
}

// MaxWriteBytes is what this transport can carry in a single write.
//
// Named separately from MaxPayloadBytes because the question a credential
// rotation asks is not "can this carry a firmware image" but "can this carry
// two kilobytes right now" — and a 240 byte LoRa frame answers no to both for
// the same underlying reason but at very different magnitudes.
func (t Transport) MaxWriteBytes() int64 {
	return t.MaxPayloadBytes
}

// DeviceCapability is the per-device capability record.
//
// A separate object rather than a field on KubeEdge's Device, because that CRD
// is upstream and this is ours. The mapper populates what it observes;
// a human declares what it cannot.
type DeviceCapability struct {
	// +optional
	Transports []Transport `json:"transports,omitempty"`

	// ReachableVia is which transports are up right now. Observed, not
	// declared — this is the field that makes capability time-varying rather
	// than static.
	// +optional
	ReachableVia []string `json:"reachableVia,omitempty"`

	// +optional
	BatteryPercent int32 `json:"batteryPercent,omitempty"`
	// +optional
	BatteryMah int32 `json:"batteryMah,omitempty"`

	// RunningConfigHash is what the device reports it is actually running.
	//
	// The health gate compares against this rather than against what the
	// device was told, because a device can acknowledge an instruction it
	// never carried out. That has already happened once in this project.
	// +optional
	RunningConfigHash string `json:"runningConfigHash,omitempty"`
}

// CanAcceptOTA answers the question the rollout planner asks, and returns the
// reason rather than just a boolean — because "no" is only useful with "why".
func (c *DeviceCapability) CanAcceptOTA(sizeBytes int64) (bool, string, string) {
	if len(c.Transports) == 0 {
		return false, ReasonNoOTATransport, "no transports declared"
	}

	var names []string
	for _, t := range c.Transports {
		names = append(names, t.Type)
		if !t.OTA {
			continue
		}
		if sizeBytes > 0 && t.MaxPayloadBytes > 0 && sizeBytes > t.MaxPayloadBytes {
			continue
		}
		return true, "", ""
	}

	detail := "transports: " + join(names, ", ") + " — none can carry firmware"
	if sizeBytes > 0 {
		return false, ReasonArtifactTooLarge, detail
	}
	return false, ReasonNoOTATransport, detail
}

// OTAAffordable checks whether the device can spare the energy.
func (c *DeviceCapability) OTAAffordable(minPercentAfter int32) (bool, string) {
	if c.BatteryPercent == 0 || minPercentAfter == 0 {
		return true, ""
	}
	var cost int32
	for _, t := range c.Transports {
		if t.OTA && t.OTACostMah > cost {
			cost = t.OTACostMah
		}
	}
	if cost == 0 || c.BatteryMah == 0 {
		return true, ""
	}
	costPercent := (cost * 100) / c.BatteryMah
	if c.BatteryPercent-costPercent < minPercentAfter {
		return false, itoa(cost) + " mAh of " + itoa(c.BatteryMah) +
			" mAh remaining (" + itoa(costPercent) + "%)"
	}
	return true, ""
}

func join(s []string, sep string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}

func itoa(i int32) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
