package internal

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// AssessDrift compares what a fleet is running against what it should be.
//
// Mechanically this is the rollout health gate run continuously rather than
// during a rollout, which is why it is cheap. What it adds is the two columns
// a bare comparison cannot produce: how long a device has been wrong, and
// whether it can be fixed without a truck.
// expectSetAt is when the expectation last changed. Grace is measured from
// there, not from a device's last contact.
//
// Measuring from lastSeen was wrong in a way the dry run made obvious: a device
// reporting the wrong hash every 30 seconds was permanently "within grace" and
// counted as converged, while a correct device that had been quiet for a day
// was not. Exactly backwards. Grace exists because a fleet mid-rollout
// legitimately disagrees for a while — that is a property of the rollout, not
// of any individual device's chattiness.
func AssessDrift(
	spec *fleetv1.FleetDriftSpec,
	livenesses []fleetv1.DeviceLiveness,
	now time.Time,
) fleetv1.FleetDriftStatus {
	return AssessDriftSince(spec, livenesses, time.Time{}, now)
}

func AssessDriftSince(
	spec *fleetv1.FleetDriftSpec,
	livenesses []fleetv1.DeviceLiveness,
	expectSetAt time.Time,
	now time.Time,
) fleetv1.FleetDriftStatus {
	out := fleetv1.FleetDriftStatus{Total: int32(len(livenesses))}
	// Fleet-wide, and computed once: either the expectation is new enough that
	// disagreement is expected, or it is not.
	inGrace := false
	if spec.GracePeriod != nil && !expectSetAt.IsZero() {
		inGrace = now.Sub(expectSetAt) < spec.GracePeriod.Duration
	}
	var oldest time.Duration

	for i := range livenesses {
		l := &livenesses[i]
		cap := l.Status.Capability

		if cap == nil || cap.RunningConfigHash == "" {
			// Counted apart from drifted on purpose. "We do not know" and "it
			// is wrong" call for different responses, and merging them makes
			// both numbers useless.
			out.Unknown++
			out.Devices = append(out.Devices, fleetv1.DriftedDevice{
				Device:     l.Name,
				Property:   "running_config_hash",
				Assessment: fleetv1.DriftStateUnknown,
			})
			continue
		}

		want := ""
		if spec.Expect != nil {
			want = spec.Expect.ConfigHash
		}
		if want == "" || cap.RunningConfigHash == want {
			out.Converged++
			continue
		}

		age := time.Duration(0)
		if l.Status.LastSeen != nil {
			age = now.Sub(l.Status.LastSeen.Time)
		}
		if inGrace {
			// The expectation changed recently and the fleet has not caught up
			// yet. Without this every rollout trips its own drift alarm on its
			// first pass.
			out.Converged++
			continue
		}

		if spec.IgnoreSleeping && l.Status.State == fleetv1.StateSleeping {
			out.Converged++
			continue
		}

		d := fleetv1.DriftedDevice{
			Device:   l.Name,
			Property: "running_config_hash",
			Expected: want,
			Actual:   cap.RunningConfigHash,
			DriftAge: shortDuration(age),
		}

		// Can this even be corrected, or is it a work order?
		ok, _, _ := cap.CanAcceptOTA(0)
		d.Remediable = ok && reachableOverOTA(cap)

		switch {
		case !ok:
			// Drifted and unfixable over anything it has. Categorically
			// different from drifted and one write away.
			d.Assessment = fleetv1.DriftNotRemediable
		case !reachableOverOTA(cap):
			// Capable, online, and the door that could fix it is shut. The
			// dry run caught this reading as DriftedAndReachable next to
			// remediable=false, which is a contradiction on one line.
			d.Assessment = fleetv1.DriftAwaitingWindow
		case l.Status.State == fleetv1.StateSleeping,
			l.Status.State == fleetv1.StateUnreachable:
			// The row this whole project is about. A battery node silent for
			// three days, whose last report disagreed, checking in every four
			// days. Drifted, unreachable, and entirely fine — and every
			// monitoring system available would have paged somebody.
			d.Assessment = fleetv1.DriftDeviceSleeping
		default:
			d.Assessment = fleetv1.DriftNeedsAttention
		}

		if age > oldest {
			oldest = age
		}
		out.Drifted++
		out.Devices = append(out.Devices, d)
	}

	if oldest > 0 {
		// The single most useful number here. A fleet 5% drifted for an hour
		// is mid-rollout; 5% drifted for three months is a process problem,
		// and the percentage looks identical.
		out.OldestDrift = shortDuration(oldest)
	}
	t := metav1.NewTime(now)
	out.LastChecked = &t
	return out
}

// AssessCredentials works out who goes dark, when, and whether anyone has to
// drive there.
//
// The interesting output is not the expiry date — every certificate manager
// has that. It is the intersection of expiry with capability: a device that
// will expire AND cannot receive its replacement over any transport it has.
//
// That number cannot be computed without knowing what a device can receive as
// opposed to whether it is online, which is why no other tool produces it.
func AssessCredentials(
	spec *fleetv1.CredentialExpirySpec,
	livenesses []fleetv1.DeviceLiveness,
	expiries map[string]time.Time,
	now time.Time,
) fleetv1.CredentialExpiryStatus {
	out := fleetv1.CredentialExpiryStatus{Total: int32(len(livenesses))}

	warn := 30 * 24 * time.Hour
	if spec.WarnBefore != nil {
		warn = spec.WarnBefore.Duration
	}
	var next time.Time

	for i := range livenesses {
		l := &livenesses[i]
		cs := fleetv1.CredentialStatus{Device: l.Name}

		exp, known := expiries[l.Name]
		if !known {
			// Arguably worse than AtRisk: at least AtRisk has a date.
			cs.State = fleetv1.CredUnknown
			cs.Reason = "device has never reported a credential expiry"
			cs.ActionRequired = "verify the device reports its expiry property"
			out.Devices = append(out.Devices, cs)
			continue
		}

		t := metav1.NewTime(exp)
		cs.ExpiresAt = &t
		left := exp.Sub(now)
		cs.TimeLeft = shortDuration(left)

		if next.IsZero() || exp.Before(next) {
			next = exp
		}

		// Can the replacement reach this device over anything it has?
		var via []string
		if cap := l.Status.Capability; cap != nil {
			for _, tr := range cap.Transports {
				if !tr.Config && !tr.OTA {
					continue
				}
				if spec.RotationSizeBytes > 0 && tr.MaxWriteBytes() > 0 &&
					spec.RotationSizeBytes > tr.MaxWriteBytes() {
					// A 2 KB certificate does not fit in a 240 byte frame.
					// Not slowly — never.
					continue
				}
				via = append(via, tr.Type)
			}
		}
		cs.RotatableVia = via
		cs.Rotatable = len(via) > 0

		switch {
		case left <= 0:
			// Already dark. Recorded rather than alerted: the moment to act
			// was months ago, and this object existed to prevent exactly this.
			cs.State = fleetv1.CredExpired
			cs.Reason = "credential expired"
			cs.ActionRequired = "device is unreachable; site visit required"
			out.Expired++

		case !cs.Rotatable:
			// The row that justifies the object. Expiry is knowable from a
			// certificate; this is knowable only from capability.
			cs.State = fleetv1.CredAtRisk
			cs.Reason = fmt.Sprintf(
				"no transport can carry a %d byte credential", spec.RotationSizeBytes)
			cs.ActionRequired = fmt.Sprintf(
				"schedule site visit before %s", exp.Format("2006-01-02"))
			out.AtRisk++

		case left <= warn:
			cs.State = fleetv1.CredExpiring
			cs.Reason = fmt.Sprintf("rotatable via %v", via)
			cs.ActionRequired = fmt.Sprintf(
				"rotate before %s", exp.Format("2006-01-02"))
			out.Expiring++

		default:
			cs.State = fleetv1.CredHealthy
			out.Healthy++
		}
		out.Devices = append(out.Devices, cs)
	}

	if !next.IsZero() {
		t := metav1.NewTime(next)
		out.NextExpiry = &t
	}
	c := metav1.NewTime(now)
	out.LastChecked = &c
	return out
}
