package driver

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// This file is the transport ladder.
//
// Everything before it assumed a device has one way in. A real field node has
// several, they are up at different times, and they can carry different things.
// A LoRa link that is always available cannot take a firmware image; a WiFi
// link that can is only there when a truck drives past.
//
// So "can I write this to this device" is not one question. It is: which doors
// are open right now, which of those can carry a payload this size, and does
// this particular property demand a specific one.

// effectiveTransports returns the declared transports, synthesising one from
// the legacy single-transport fields when none are declared.
//
// The synthesis matters more than it looks: every device deployed before this
// change keeps working, and the new code path is the only one anyone has to
// reason about.
func (c *CustomizedClient) effectiveTransports() []TransportConfig {
	if len(c.Transports) > 0 {
		return c.Transports
	}
	if c.Gateway != "" {
		return []TransportConfig{{
			Type:              "lora",
			Gateway:           c.Gateway,
			NodeID:            c.NodeID,
			Config:            true,
			OTA:               false,
			MaxWriteBytes:     240,
			StaleAfterSeconds: c.ExpectedIntervalSeconds * 3,
		}}
	}
	return []TransportConfig{{
		Type:        "wifi",
		TopicPrefix: c.TopicPrefix,
		Config:      true,
		OTA:         true,
	}}
}

// isUp answers whether a transport has carried traffic recently enough to
// believe it is still there.
//
// The staleness check is the whole reason this exists. Reachability derived
// from "we saw it once" is believed forever, and a planner routing firmware to
// a WiFi link that went down on Tuesday produces a rollout that hangs rather
// than one that waits — and those look identical from outside.
//
// A transport with no staleness configured is treated as always up, which is
// right for a wired link and wrong for a radio. Declare the field on radios.
func (c *CustomizedClient) isUp(t TransportConfig) bool {
	if t.StaleAfterSeconds <= 0 {
		return true
	}
	c.cacheMu.RLock()
	last, seen := c.lastSeenPer[t.Type]
	c.cacheMu.RUnlock()

	if !seen {
		// Never seen. Not the same as down — a device that has just been
		// created has no history, and refusing on that basis would make every
		// rollout wait for a heartbeat that a sleeping node will not send for
		// hours. Optimism here is corrected by the write failing.
		return true
	}
	return time.Since(last) < time.Duration(t.StaleAfterSeconds)*time.Second
}

// markSeen records traffic on a transport. Called from both message handlers,
// which is what keeps liveness per-door rather than per-device.
func (c *CustomizedClient) markSeen(transport string) {
	c.cacheMu.Lock()
	if c.lastSeenPer == nil {
		c.lastSeenPer = make(map[string]time.Time)
	}
	c.lastSeenPer[transport] = time.Now()
	c.cacheMu.Unlock()
}

// selectTransport picks a door for one write, and explains itself when it
// cannot.
//
// Order of checks is deliberate and mirrors the controller's planner:
// permanent facts before temporary ones. A property that requires OTA over a
// device with no OTA-capable transport is refused; the same property over a
// device whose OTA transport is currently down is deferred. Those are
// different sentences and the caller needs to tell them apart.
func (c *CustomizedClient) selectTransport(v *VisitorConfig, payloadSize int) (*TransportConfig, error) {
	transports := c.effectiveTransports()
	if len(transports) == 0 {
		return nil, fmt.Errorf("esphome-mqtt: %s has no transports declared", c.subject())
	}

	var (
		capable []TransportConfig // could carry this, in principle
		names   []string
	)
	for _, t := range transports {
		names = append(names, t.Type)

		if v.RequiresTransport != "" && t.Type != v.RequiresTransport {
			continue
		}
		if !t.Config && !t.OTA {
			continue
		}
		// A firmware write is pinned to an OTA-capable transport. Without this
		// the mapper would happily push a URL over LoRa, where it would be
		// truncated at 240 bytes and fail in a way that looks like a radio
		// problem.
		if v.RequiresTransport == "ota" && !t.OTA {
			continue
		}
		if t.MaxWriteBytes > 0 && payloadSize > t.MaxWriteBytes {
			continue
		}
		capable = append(capable, t)
	}

	if len(capable) == 0 {
		// Permanent. No transport on this device can ever carry this, so
		// saying "try later" would be a lie.
		return nil, fmt.Errorf(
			"esphome-mqtt: %s cannot carry a %d byte write on any transport "+
				"(have: %s) — this property is unavailable on this device",
			c.subject(), payloadSize, strings.Join(names, ", "))
	}

	for _, t := range capable {
		if c.isUp(t) {
			return &t, nil
		}
	}

	// Temporary. Capable, but every capable door is shut right now. This is
	// the AwaitingTransportWindow case, and it is not a failure — a node that
	// meets WiFi when a truck drives past is waiting for the truck.
	var capableNames []string
	for _, t := range capable {
		capableNames = append(capableNames, t.Type)
	}
	return nil, fmt.Errorf(
		"esphome-mqtt: %s is capable over %s but none are up right now — "+
			"deferring until next in range",
		c.subject(), strings.Join(capableNames, ", "))
}

// writeVia publishes on a specific transport. Only directly-addressed
// transports have a write path today: relaying a downlink through a gateway
// needs firmware support that does not exist yet, and pretending otherwise
// would turn a clean refusal into a silent drop.
func (c *CustomizedClient) writeVia(
	t *TransportConfig, v *VisitorConfig, payload string,
) error {
	if t.TopicPrefix == "" {
		return fmt.Errorf(
			"esphome-mqtt: transport %s reaches %s through gateway %s, "+
				"which has no downlink path",
			t.Type, c.subject(), t.Gateway)
	}
	topic := t.TopicPrefix + "/" + strings.TrimPrefix(v.CommandTopic, "/")

	c.deviceMutex.Lock()
	defer c.deviceMutex.Unlock()

	token := c.client.Publish(topic, v.QoS, v.Retain, payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("esphome-mqtt: timed out publishing to %s", topic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("esphome-mqtt: publish to %s: %w", topic, err)
	}
	klog.V(3).Infof("esphome-mqtt: wrote %s = %s via %s", topic, payload, t.Type)
	return nil
}
