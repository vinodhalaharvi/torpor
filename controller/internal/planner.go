package internal

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// Plan is the answer to "what would happen if I ran this", computed before
// anything is transmitted.
//
// This is the piece that does not exist anywhere else. Argo Rollouts, Memfault
// and Golioth all attempt and then report what failed; the distinction between
// "cannot" and "not yet" and "did not" is only available afterwards, as three
// flavours of timeout. Here it is available first, because capability is
// declared rather than discovered.
type Plan struct {
	Eligible []string
	Refused  []fleetv1.DeviceOutcome
	Pending  []fleetv1.DeviceOutcome
}

// PlanRollout sorts a fleet into can, cannot, and not-yet.
//
// Order matters. Capability is checked before reachability, because a device
// that is structurally incapable is refused whether or not it happens to be
// awake — and telling someone their LoRa node is "pending" when it will never
// be eligible is worse than telling them nothing.
func PlanRollout(
	rollout *fleetv1.FirmwareRollout,
	livenesses []fleetv1.DeviceLiveness,
) Plan {
	var p Plan
	req := rollout.Spec.Requires

	for i := range livenesses {
		l := &livenesses[i]
		name := l.Name
		cap := l.Status.Capability

		// --- 1. Capability. Permanent facts first. -----------------------
		if req != nil && req.OTA {
			if cap == nil {
				p.Refused = append(p.Refused, outcome(name,
					fleetv1.ReasonNoOTATransport,
					"no capability recorded for this device"))
				continue
			}
			ok, reason, detail := cap.CanAcceptOTA(rollout.Spec.Source.SizeBytes)
			if !ok {
				p.Refused = append(p.Refused, outcome(name, reason, detail))
				continue
			}
			if req.MinBatteryPercent > 0 {
				if affordable, detail := cap.OTAAffordable(req.MinBatteryPercent); !affordable {
					p.Refused = append(p.Refused, outcome(name,
						fleetv1.ReasonBatteryTooLow, detail))
					continue
				}
			}
		}

		// --- 2. Reachability. Temporary facts second. --------------------
		//
		// Everything below is Pending rather than Refused, and the difference
		// is not cosmetic: Refused means stop asking, Pending means ask again
		// later. A rollout can sit in Pending for days and be entirely on
		// track, which is a sentence no other tool can say.
		switch l.Status.State {
		case fleetv1.StateUnreachable:
			p.Pending = append(p.Pending, outcomeSince(name,
				fleetv1.ReasonUnreachable,
				fmt.Sprintf("no report for %s", l.Status.SilentFor),
				l.Status.LastSeen))
			continue

		case fleetv1.StateSleeping:
			// Asleep is not a problem. It is what the device is for.
			p.Pending = append(p.Pending, outcomeSince(name,
				fleetv1.ReasonAsleep,
				fmt.Sprintf("silent %s, next expected by %s",
					l.Status.SilentFor, fmtTime(l.Status.NextExpectedBy)),
				l.Status.LastSeen))
			continue

		case fleetv1.StateUnknown:
			p.Pending = append(p.Pending, outcome(name,
				fleetv1.ReasonUnreachable, "never reported"))
			continue
		}

		// Reachable now, but not over a transport that can carry firmware.
		// A node that is LoRa-always and WiFi-occasionally lives here between
		// truck visits: capable, online, and still unable to take this.
		if req != nil && req.OTA && cap != nil && !reachableOverOTA(cap) {
			p.Pending = append(p.Pending, outcome(name,
				fleetv1.ReasonAwaitingWindow,
				fmt.Sprintf("reachable via %v, none OTA-capable", cap.ReachableVia)))
			continue
		}

		if req != nil && req.MaxSilentFor != nil && l.Status.LastSeen != nil {
			if time.Since(l.Status.LastSeen.Time) > req.MaxSilentFor.Duration {
				p.Pending = append(p.Pending, outcomeSince(name,
					fleetv1.ReasonUnreachable,
					fmt.Sprintf("silent %s, limit %s",
						l.Status.SilentFor, req.MaxSilentFor.Duration),
					l.Status.LastSeen))
				continue
			}
		}

		p.Eligible = append(p.Eligible, name)
	}
	return p
}

// reachableOverOTA asks whether any transport that is up right now can carry
// firmware — not whether the device could in principle.
func reachableOverOTA(c *fleetv1.DeviceCapability) bool {
	if len(c.ReachableVia) == 0 {
		return true // nothing observed; do not refuse on missing data
	}
	for _, via := range c.ReachableVia {
		for _, t := range c.Transports {
			if t.Type == via && t.OTA {
				return true
			}
		}
	}
	return false
}

// StepSize converts a cumulative percentage into a device count.
//
// Steps are cumulative like Argo Rollouts — [1, 10, 50, 100] means 1% then
// 10% then 50% then everything — and the percentage is of ELIGIBLE devices,
// not of target. Rolling to "50%" of a fleet where half is refused should
// reach half of what can actually take it, not a quarter.
func StepSize(eligible int, percent int32) int {
	n := (eligible * int(percent)) / 100
	if n < 1 && percent > 0 {
		n = 1
	}
	if n > eligible {
		n = eligible
	}
	return n
}

func outcome(device, reason, detail string) fleetv1.DeviceOutcome {
	return fleetv1.DeviceOutcome{Device: device, Reason: reason, Detail: detail}
}

func outcomeSince(device, reason, detail string, since *metav1.Time) fleetv1.DeviceOutcome {
	o := outcome(device, reason, detail)
	o.Since = since
	return o
}

func fmtTime(t *metav1.Time) string {
	if t == nil {
		return "unknown"
	}
	return t.Format(time.RFC3339)
}
