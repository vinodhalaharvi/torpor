package internal

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// EvaluateQuery runs a FleetQuery over the liveness objects.
//
// Everything it selects on is derived — what a device is running, whether it
// can be reached, what it is capable of. None of it is a label, because none
// of it is something a person decided, and a label recording a fact the world
// controls is a label that will be wrong.
func EvaluateQuery(
	spec *fleetv1.FleetQuerySpec,
	livenesses []fleetv1.DeviceLiveness,
	now time.Time,
) fleetv1.FleetQueryStatus {
	out := fleetv1.FleetQueryStatus{
		Evaluated: int32(len(livenesses)),
		Summary:   map[string]int32{},
	}
	limit := int(spec.Limit)
	if limit <= 0 {
		limit = 100
	}

	for i := range livenesses {
		l := &livenesses[i]
		if spec.Where != nil && !matches(spec.Where, l, now) {
			continue
		}
		out.Matched++

		r := fleetv1.QueryResult{
			Device:    l.Name,
			State:     string(l.Status.State),
			Transport: l.Status.Transport,
			SilentFor: l.Status.SilentFor,
		}
		if c := l.Status.Capability; c != nil {
			r.RunningConfigHash = c.RunningConfigHash
		}

		// Actionable is what turns a list into a plan. A count of drifted
		// devices prompts "and how many can I actually fix" — which decides
		// whether this is a rollout or a truck schedule.
		r.Actionable, r.Reason = actionable(l)

		key := r.Reason
		if r.Actionable {
			key = "actionable"
		}
		out.Summary[key]++

		if len(out.Devices) < limit {
			out.Devices = append(out.Devices, r)
		} else {
			// Counts stay accurate while the list is truncated, and the flag
			// says so — nobody should build a rollout from a partial answer
			// while believing it is complete.
			out.Truncated = true
		}
	}

	t := metav1.NewTime(now)
	out.LastEvaluated = &t
	return out
}

func actionable(l *fleetv1.DeviceLiveness) (bool, string) {
	switch l.Status.State {
	case fleetv1.StateUnreachable:
		return false, "unreachable"
	case fleetv1.StateUnknown:
		return false, "never reported"
	case fleetv1.StateSleeping:
		// Not actionable *now*, and not a problem. The distinction between
		// this and unreachable is the difference between waiting and worrying.
		return false, "sleeping"
	}
	c := l.Status.Capability
	if c == nil {
		return false, "capability unknown"
	}
	if ok, _, _ := c.CanAcceptOTA(0); !ok {
		return false, "no ota-capable transport"
	}
	if !reachableOverOTA(c) {
		return false, "ota transport not up"
	}
	return true, ""
}

func matches(p *fleetv1.QueryPredicate, l *fleetv1.DeviceLiveness, now time.Time) bool {
	if p.Not != nil && matches(p.Not, l, now) {
		return false
	}

	if p.State != nil && !strMatch(p.State, string(l.Status.State)) {
		return false
	}

	cap := l.Status.Capability

	if p.Transport != nil {
		var declared []string
		if cap != nil {
			for _, t := range cap.Transports {
				declared = append(declared, t.Type)
			}
		}
		if !anyMatch(p.Transport, declared) {
			return false
		}
	}

	// Declared and available are different questions. A node with WiFi in its
	// transport list is not a node with WiFi up.
	if p.ReachableVia != nil {
		var via []string
		if cap != nil {
			via = cap.ReachableVia
		}
		if !anyMatch(p.ReachableVia, via) {
			return false
		}
	}

	if p.RunningConfigHash != nil {
		h := ""
		if cap != nil {
			h = cap.RunningConfigHash
		}
		if !strMatch(p.RunningConfigHash, h) {
			return false
		}
	}

	if p.OTACapable != nil {
		ok := false
		if cap != nil {
			ok, _, _ = cap.CanAcceptOTA(0)
		}
		if ok != *p.OTACapable {
			return false
		}
	}

	if p.OTAReachable != nil {
		ok := false
		if cap != nil {
			if c, _, _ := cap.CanAcceptOTA(0); c && reachableOverOTA(cap) {
				ok = true
			}
		}
		if ok != *p.OTAReachable {
			return false
		}
	}

	if p.SilentLongerThan != nil {
		if l.Status.LastSeen == nil {
			return false
		}
		if now.Sub(l.Status.LastSeen.Time) < p.SilentLongerThan.Duration {
			return false
		}
	}

	// Against the device's own expectation rather than the wall clock.
	//
	// A daily-checkin node silent for six hours is fine; an every-30s node
	// silent for six hours is gone. A fleet-wide wall-clock threshold cannot
	// tell those apart and will be wrong in both directions at once.
	if p.SilentBeyondExpected != nil {
		beyond := l.Status.State == fleetv1.StateUnreachable
		if beyond != *p.SilentBeyondExpected {
			return false
		}
	}

	if p.BatteryBelowPercent != nil {
		if cap == nil || cap.BatteryPercent == 0 {
			return false // unknown battery is not "below"; do not guess
		}
		if cap.BatteryPercent >= *p.BatteryBelowPercent {
			return false
		}
	}

	return true
}

func strMatch(m *fleetv1.StringMatch, v string) bool {
	if m.Equals != "" && v != m.Equals {
		return false
	}
	if len(m.In) > 0 && !contains(m.In, v) {
		return false
	}
	if len(m.NotIn) > 0 && contains(m.NotIn, v) {
		return false
	}
	return true
}

// anyMatch applies a StringMatch to a set. Semantics chosen so notIn means
// "none of these", which is what someone writing it expects — a device with
// both lora and wifi should not match notIn: [wifi].
func anyMatch(m *fleetv1.StringMatch, vals []string) bool {
	if len(m.NotIn) > 0 {
		for _, v := range vals {
			if contains(m.NotIn, v) {
				return false
			}
		}
	}
	if m.Equals == "" && len(m.In) == 0 {
		return true
	}
	for _, v := range vals {
		if m.Equals != "" && v == m.Equals {
			return true
		}
		if len(m.In) > 0 && contains(m.In, v) {
			return true
		}
	}
	return false
}

// QuerySummaryLine renders the summary as the sentence somebody actually wants.
func QuerySummaryLine(st *fleetv1.FleetQueryStatus) string {
	act := st.Summary["actionable"]
	return fmt.Sprintf("%d matched, %d actionable now, %d need something else",
		st.Matched, act, st.Matched-act)
}
