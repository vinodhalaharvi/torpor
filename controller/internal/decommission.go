package internal

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// DecommissionSet is the set of devices no longer part of the live fleet.
//
// Every derived object filters through this. Without it a stolen device is
// counted forever — in drift totals, in credential AtRisk, in every query —
// and six months of accumulated dead devices makes the numbers useless. Which
// makes them ignored, which is the real failure: a fleet health metric nobody
// trusts is worse than no metric.
type DecommissionSet map[string]*fleetv1.DeviceDecommission

func NewDecommissionSet(items []fleetv1.DeviceDecommission) DecommissionSet {
	s := DecommissionSet{}
	for i := range items {
		d := &items[i]
		if d.Status.Phase == fleetv1.DecomComplete {
			s[d.Spec.DeviceRef] = d
		}
	}
	return s
}

func (s DecommissionSet) Has(device string) bool {
	_, ok := s[device]
	return ok
}

// Filter removes decommissioned devices from a liveness list.
//
// Called by every derived controller. The single most important line in this
// file is that it is called at all — the failure mode here is not a bug, it is
// somebody adding a new derived object in six months and forgetting to filter.
func (s DecommissionSet) Filter(in []fleetv1.DeviceLiveness) []fleetv1.DeviceLiveness {
	if len(s) == 0 {
		return in
	}
	out := make([]fleetv1.DeviceLiveness, 0, len(in))
	for _, l := range in {
		if !s.Has(l.Name) {
			out = append(out, l)
		}
	}
	return out
}

// BuildDecommissionStatus captures what is worth keeping before the device
// stops being counted.
//
// Everything recorded here is unavailable afterwards. Service life is the
// question anybody procuring the next batch will ask, and the final config
// hash is occasionally the most important field in an incident — three devices
// dying within a week of each other on the same firmware is a pattern that is
// invisible once the objects are gone.
func BuildDecommissionStatus(
	spec *fleetv1.DeviceDecommissionSpec,
	live *fleetv1.DeviceLiveness,
	firstSeen *time.Time,
	now time.Time,
) fleetv1.DeviceDecommissionStatus {
	st := fleetv1.DeviceDecommissionStatus{
		Phase:               fleetv1.DecomComplete,
		ExcludedFromQueries: true,
	}

	at := now
	if spec.EffectiveFrom != nil {
		// Backdated, because the paperwork always lags the event. A device
		// stolen on Tuesday and noticed on Friday should not leave three days
		// of unexplained unreachability in the drift history.
		at = spec.EffectiveFrom.Time
	}
	t := metav1.NewTime(at)
	st.DecommissionedAt = &t

	if live != nil {
		if live.Status.LastSeen != nil {
			ls := *live.Status.LastSeen
			st.LastSeenAlive = &ls
			if firstSeen != nil {
				st.ServiceLife = shortDuration(live.Status.LastSeen.Time.Sub(*firstSeen))
			}
		}
		if c := live.Status.Capability; c != nil {
			st.FinalConfigHash = c.RunningConfigHash
		}
	}

	// Default true. A device removed for theft that keeps working credentials
	// is a security problem rather than a bookkeeping one, and the safe
	// default is the one you would pick while calm.
	revoke := true
	if spec.RevokeCredentials != nil {
		revoke = *spec.RevokeCredentials
	}
	st.CredentialsRevoked = revoke

	return st
}

// CheckDecommissionBlockers reports anything that would fight a decommission.
//
// Reported rather than forced. Silently decommissioning a device that a
// template will recreate in thirty seconds produces a loop nobody can see, and
// an object stuck in Blocked with a reason is far easier to debug than a
// device that keeps coming back.
func CheckDecommissionBlockers(
	device string,
	activeRollouts []fleetv1.FirmwareRollout,
	templateInstances map[string][]string, // template name -> instance names
) []string {
	var blockers []string

	for i := range activeRollouts {
		ro := &activeRollouts[i]
		switch ro.Status.Phase {
		case fleetv1.PhaseComplete, fleetv1.PhaseRolledBack:
			continue
		}
		for _, o := range ro.Status.Pending {
			if o.Device == device {
				blockers = append(blockers, fmt.Sprintf(
					"rollout %s has this device pending", ro.Name))
			}
		}
	}

	for tmpl, instances := range templateInstances {
		for _, name := range instances {
			if name == device {
				blockers = append(blockers, fmt.Sprintf(
					"template %s would recreate this device; remove the instance first", tmpl))
			}
		}
	}
	return blockers
}

// DecommissionSummary groups by reason.
//
// The output is a procurement question rather than an ops one. "Nine failed and
// four were stolen" is a different conversation from "thirteen devices left the
// fleet", and only one of them leads anywhere.
func DecommissionSummary(items []fleetv1.DeviceDecommission) map[string]int32 {
	out := map[string]int32{}
	for i := range items {
		if items[i].Status.Phase != fleetv1.DecomComplete {
			continue
		}
		out[string(items[i].Spec.Reason)]++
	}
	return out
}
