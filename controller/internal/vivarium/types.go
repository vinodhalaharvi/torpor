// Package vivarium keeps devices under observation.
//
// Not a simulator. A simulator models behaviour approximately for study; these
// speak real MQTT on a real broker, and the mapper cannot tell them from a
// board. Not quite an emulator either, since nothing is being reproduced
// instruction for instruction. It is an enclosure you keep organisms in so you
// can watch what they do — which is what a vivarium is.
//
// The reason to build one carefully: a control plane whose entire value is
// making good decisions about devices that misbehave cannot be trusted until
// it has been shown devices that misbehave. Hardware gives you two boards on a
// desk that mostly work. This gives you forty that fail the way real fleets
// fail.
package vivarium

import (
	"fmt"
	"strings"
	"math/rand"
	"time"
)

// Fleet is a whole enclosure.
type Fleet struct {
	// Broker to publish on. Empty starts an embedded one, which is the point:
	// a vivarium that needs infrastructure set up first is a vivarium nobody
	// runs.
	Broker string `json:"broker,omitempty"`

	// Seed makes a run reproducible.
	//
	// The single most important field here. A simulator you cannot replay
	// generates anecdotes — "it failed once yesterday" is not a bug report.
	// With a seed, a failing run is a fixture.
	Seed int64 `json:"seed,omitempty"`

	// Speed multiplies the passage of time. 60 means a 30-minute reporting
	// interval fires every 30 seconds, so a day of fleet behaviour takes 24
	// minutes to watch.
	//
	// Necessary because the interesting states are slow. A device that reports
	// daily takes four days to become Unreachable at a stale multiplier of
	// three, and nobody debugs a control plane on that schedule.
	Speed float64 `json:"speed,omitempty"`

	// Cohorts are groups that fail together.
	Cohorts []Cohort `json:"cohorts,omitempty"`

	// Gateways relay for devices behind them, and take those devices down
	// with them when they fail.
	Gateways []Gateway `json:"gateways,omitempty"`

	Devices []Device `json:"devices"`
}

// Cohort is a set of devices that share a failure mode.
//
// This is the half that matters. Independent per-device probability is easy
// and produces a fleet where a canary tells you nothing about the next device,
// because nothing is correlated — which is a fleet where a canary is
// pointless, and therefore a fleet that cannot test whether the canary works.
//
// Real failures are correlated. Bad firmware does not fail 2% of devices at
// random; it fails every device on hardware revision B, or every device that
// boots below −10°C. That is precisely the case the health gate exists to
// catch, and the only case where stopping at the first failure is right.
type Cohort struct {
	Name string `json:"name"`

	// FirmwareFailsFor lists config hashes this cohort cannot run. Every
	// member fails on those, deterministically — not probabilistically,
	// because a hardware incompatibility is not a coin flip.
	FirmwareFailsFor []string `json:"firmwareFailsFor,omitempty"`

	// FailureMode is how they fail. Silence and boot-looping and lying about
	// what they are running look identical to a device that simply worked,
	// right up until they do not.
	FailureMode FailureMode `json:"failureMode,omitempty"`
}

type Gateway struct {
	Name string `json:"name"`

	// OutageEvery and OutageFor give a gateway a duty cycle of its own.
	//
	// A gateway outage is the most correlated failure there is: forty devices
	// go silent in the same second, and the right response is to look at one
	// thing rather than forty. A liveness controller that reports forty
	// separate Unreachable devices has technically told the truth and
	// practically told you nothing.
	OutageEvery Duration `json:"outageEvery,omitempty"`
	OutageFor   Duration `json:"outageFor,omitempty"`
}

// Device is one organism.
type Device struct {
	Name string `json:"name"`

	// TopicPrefix for a directly-addressed device. Empty means it is only
	// reachable through a gateway, which is the LoRa case.
	TopicPrefix string `json:"topicPrefix,omitempty"`

	// Gateway and NodeID for a relayed device.
	Gateway string `json:"gateway,omitempty"`
	NodeID  int    `json:"nodeID,omitempty"`

	Cohort string `json:"cohort,omitempty"`

	// ReportEvery is how often it speaks unprompted.
	ReportEvery Duration `json:"reportEvery,omitempty"`

	// WiFiUpFor and WiFiEvery give an opportunistic transport its window.
	// A node that meets WiFi when a truck drives past is up for four minutes
	// a day, and that window is the difference between a device a rollout can
	// reach and one it cannot.
	WiFiUpFor Duration `json:"wifiUpFor,omitempty"`
	WiFiEvery Duration `json:"wifiEvery,omitempty"`

	ConfigHash string  `json:"configHash,omitempty"`
	Battery    float64 `json:"battery,omitempty"`

	// BatteryDrainPerDay, so a fleet ages. Without it every device is at 100%
	// forever and the battery refusal never fires.
	BatteryDrainPerDay float64 `json:"batteryDrainPerDay,omitempty"`

	// CertExpiresAt, for the credential path.
	CertExpiresAt string `json:"certExpiresAt,omitempty"`

	Behaviour Behaviour `json:"behaviour,omitempty"`
}

// Behaviour is how one device misbehaves independently of the others.
type Behaviour struct {
	// PacketLoss drops published messages. Independent per message.
	PacketLoss float64 `json:"packetLoss,omitempty"`

	// Jitter varies the reporting interval, because nothing real is punctual
	// and a controller that only works against punctual devices does not work.
	Jitter float64 `json:"jitter,omitempty"`

	// OnFirmware is what this device does when written a firmware URL.
	OnFirmware FailureMode `json:"onFirmware,omitempty"`

	// FirmwareDelay is how long the flash takes. A rollout with a five-minute
	// health gate against a device that takes six is a rollout that fails for
	// a reason nobody would guess from the logs.
	FirmwareDelay Duration `json:"firmwareDelay,omitempty"`

	// FailChance applies OnFirmware only this fraction of the time. Zero means
	// always — which is what you want for a cohort, and not what you want for
	// flakiness.
	FailChance float64 `json:"failChance,omitempty"`
}

// FailureMode enumerates the ways a device can be wrong.
//
// Every one of these has been observed on real hardware in this project or is
// a documented failure of the platforms this replaces. None is invented for
// the sake of variety.
type FailureMode string

const (
	// Accepts and runs it. The boring case, and the majority.
	FailNone FailureMode = ""

	// Accepts the write, reports the new hash, and does not run it.
	//
	// The one that actually happened here, with a tone that never played while
	// the twin converged, the broker round-tripped cleanly and kubectl was
	// happy. Every observable signal said success. This is why the health gate
	// compares against what a device reports it is RUNNING — and this mode is
	// how you check that the gate would have caught it.
	FailReportsWithoutRunning FailureMode = "reportsWithoutRunning"

	// Takes the firmware, boots, reports healthy, then reboots repeatedly.
	// A single check taken immediately after flashing calls this healthy,
	// which is why settleFor exists.
	FailBootLoop FailureMode = "bootLoop"

	// Takes the firmware and never speaks again. The failure that costs a
	// truck roll.
	FailBricks FailureMode = "bricks"

	// Ignores the write entirely. Looks like packet loss, is not.
	FailIgnores FailureMode = "ignores"

	// Reports a hash that is neither the old nor the new one. Rare, and it
	// breaks any logic that assumes two states.
	FailWrongHash FailureMode = "wrongHash"

	// Goes quiet without warning and comes back later. Distinguishes a
	// controller that waits from one that gives up.
	FailIntermittent FailureMode = "intermittent"
)

// Duration wraps time.Duration with YAML-friendly parsing, including a day
// unit — because Go durations have no day and a fleet's timescales are days.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" || s == "null" {
		return nil
	}
	// Days first, since Go cannot parse them.
	//
	// The suffix check is not optional. fmt.Sscanf("5m", "%fd", &days) returns
	// n=1 — %f matched the 5 — even though the literal 'd' never matched the
	// 'm'. Without the check every duration in the file parsed as days, so
	// "5m" became 120h and every device waited five days to say anything.
	// The verifier found it in thirty seconds; nothing else had.
	if strings.HasSuffix(s, "d") {
		var days float64
		if n, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%f", &days); n == 1 && err == nil && days > 0 {
			d.Duration = time.Duration(days * 24 * float64(time.Hour))
			return nil
		}
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("bad duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

func (d Duration) Or(def time.Duration) time.Duration {
	if d.Duration == 0 {
		return def
	}
	return d.Duration
}

// rng is per-device and seeded from the fleet seed plus the device name, so
// adding a device to a config does not reshuffle every other device's
// behaviour. Reproducibility that breaks when you edit the file is not
// reproducibility.
func rngFor(seed int64, name string) *rand.Rand {
	var h uint64 = 14695981039346656037 // FNV-1a offset basis
	for _, c := range name {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return rand.New(rand.NewSource(seed ^ int64(h)))
}
