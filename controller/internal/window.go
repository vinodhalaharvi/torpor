package internal

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Maintenance windows — policy
// ---------------------------------------------------------------------------

// WindowState is whether we are permitted to start something right now, and if
// not, when we will be.
type WindowState struct {
	Open      bool
	Reason    string
	NextOpen  *time.Time
	NextClose *time.Time
}

// EvaluateWindow decides whether a maintenance window is open.
//
// Deny beats Allow, always. A change freeze that could be overridden by a
// broader allow rule is not a change freeze, and the person who wrote the
// freeze is usually more senior than the person who wrote the schedule.
func EvaluateWindow(spec *fleetv1.MaintenanceWindowSpec, now time.Time) WindowState {
	loc := time.UTC
	if spec.Timezone != "" {
		if l, err := time.LoadLocation(spec.Timezone); err == nil {
			loc = l
		}
		// A bad timezone falls back to UTC rather than failing closed.
		// Debatable: failing closed would be safer, but a typo in an optional
		// field silently freezing a fleet is its own kind of outage.
	}
	local := now.In(loc)

	for _, d := range spec.Deny {
		if inPeriod(d, local) {
			return WindowState{
				Open:      false,
				Reason:    reasonOr(d, "denied by policy"),
				NextOpen:  periodEnd(d, local),
				NextClose: nil,
			}
		}
	}

	// No allow rules means always open. Right for a bench, wrong for a water
	// utility — which is why the object is opt-in rather than defaulted.
	if len(spec.Allow) == 0 {
		return WindowState{Open: true, Reason: "no schedule configured"}
	}

	for _, a := range spec.Allow {
		if inPeriod(a, local) {
			return WindowState{
				Open:      true,
				Reason:    reasonOr(a, "within allowed window"),
				NextClose: periodEnd(a, local),
			}
		}
	}

	next := nextOpening(spec.Allow, local)
	return WindowState{
		Open:     false,
		Reason:   "outside all allowed windows",
		NextOpen: next,
	}
}

func inPeriod(p fleetv1.WindowPeriod, now time.Time) bool {
	if p.From != nil && now.Before(p.From.Time) {
		return false
	}
	if p.Until != nil && now.After(p.Until.Time) {
		return false
	}
	// An absolute period with no cron is simply its own bounds.
	if p.Cron == "" {
		return p.From != nil || p.Until != nil
	}
	start, ok := lastCronFire(p.Cron, now)
	if !ok {
		return false
	}
	d := time.Hour
	if p.Duration != nil {
		d = p.Duration.Duration
	}
	return now.Before(start.Add(d))
}

func periodEnd(p fleetv1.WindowPeriod, now time.Time) *time.Time {
	if p.Until != nil {
		t := p.Until.Time
		return &t
	}
	if p.Cron == "" {
		return nil
	}
	start, ok := lastCronFire(p.Cron, now)
	if !ok {
		return nil
	}
	d := time.Hour
	if p.Duration != nil {
		d = p.Duration.Duration
	}
	t := start.Add(d)
	return &t
}

func nextOpening(periods []fleetv1.WindowPeriod, now time.Time) *time.Time {
	var best *time.Time
	for _, p := range periods {
		if p.From != nil && now.Before(p.From.Time) {
			t := p.From.Time
			if best == nil || t.Before(*best) {
				best = &t
			}
			continue
		}
		if p.Cron == "" {
			continue
		}
		if t, ok := nextCronFire(p.Cron, now); ok {
			if best == nil || t.Before(*best) {
				tt := t
				best = &tt
			}
		}
	}
	return best
}

// --- minimal cron ----------------------------------------------------------
//
// Five fields, supporting * and plain numbers. Deliberately not a full cron
// implementation: ranges and steps invite schedules nobody can read at 3am,
// and a maintenance window that is hard to read is a maintenance window that
// is wrong.

type cronSpec struct{ min, hour, dom, month, dow int } // -1 means *

func parseCron(expr string) (cronSpec, bool) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return cronSpec{}, false
	}
	v := make([]int, 5)
	for i, s := range f {
		if s == "*" {
			v[i] = -1
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return cronSpec{}, false
		}
		v[i] = n
	}
	return cronSpec{v[0], v[1], v[2], v[3], v[4]}, true
}

func (c cronSpec) matches(t time.Time) bool {
	return (c.min == -1 || c.min == t.Minute()) &&
		(c.hour == -1 || c.hour == t.Hour()) &&
		(c.dom == -1 || c.dom == t.Day()) &&
		(c.month == -1 || c.month == int(t.Month())) &&
		(c.dow == -1 || c.dow == int(t.Weekday()))
}

// lastCronFire walks back a day at minute resolution. Cheap enough at the
// rates this runs, and obvious enough to reason about.
func lastCronFire(expr string, now time.Time) (time.Time, bool) {
	c, ok := parseCron(expr)
	if !ok {
		return time.Time{}, false
	}
	t := now.Truncate(time.Minute)
	for i := 0; i < 60*24*2; i++ {
		if c.matches(t) {
			return t, true
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}, false
}

func nextCronFire(expr string, now time.Time) (time.Time, bool) {
	c, ok := parseCron(expr)
	if !ok {
		return time.Time{}, false
	}
	t := now.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 60*24*370; i++ {
		if c.matches(t) {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

func reasonOr(p fleetv1.WindowPeriod, fallback string) string {
	if p.Reason != "" {
		return p.Reason
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Contact windows — physics
// ---------------------------------------------------------------------------

// LearnContactWindow infers when a device tends to be reachable, from when it
// has been.
//
// The output is a prediction and is labelled as one. That labelling is the
// point: a scheduler that treats a guess as a fact schedules a rollout for
// 04:00 and reports failure at 04:05 because the truck was late. A
// low-confidence window means wait and watch, not act then.
func LearnContactWindow(transport string, contacts []time.Time, now time.Time) fleetv1.ContactWindow {
	w := fleetv1.ContactWindow{
		Transport:       transport,
		SamplesObserved: int32(len(contacts)),
		Confidence:      fleetv1.ConfidenceNone,
	}
	if len(contacts) < 2 {
		// One contact is not a pattern. Saying so beats extrapolating from it.
		return w
	}

	var gaps []time.Duration
	for i := 1; i < len(contacts); i++ {
		if g := contacts[i].Sub(contacts[i-1]); g > 0 {
			gaps = append(gaps, g)
		}
	}
	if len(gaps) == 0 {
		return w
	}

	var total time.Duration
	for _, g := range gaps {
		total += g
	}
	mean := total / time.Duration(len(gaps))
	w.TypicalInterval = &metav1.Duration{Duration: mean}

	// Coefficient of variation, which is the honest measure here: a node that
	// checks in every 4 hours give or take a minute is predictable, and one
	// that checks in every 4 hours give or take 3 hours is not, and the mean
	// is identical.
	var sumSq float64
	for _, g := range gaps {
		d := float64(g - mean)
		sumSq += d * d
	}
	stddev := time.Duration(sqrt(sumSq / float64(len(gaps))))
	cv := 1.0
	if mean > 0 {
		cv = float64(stddev) / float64(mean)
	}

	switch {
	case len(gaps) >= 10 && cv < 0.15:
		w.Confidence = fleetv1.ConfidenceHigh
	case len(gaps) >= 5 && cv < 0.35:
		w.Confidence = fleetv1.ConfidenceMedium
	case len(gaps) >= 3:
		w.Confidence = fleetv1.ConfidenceLow
	default:
		w.Confidence = fleetv1.ConfidenceNone
	}

	if w.Confidence != fleetv1.ConfidenceNone {
		next := contacts[len(contacts)-1].Add(mean)
		// A prediction already in the past is not a prediction. It means the
		// device is overdue, which the liveness state already says better.
		if next.After(now) {
			t := metav1.NewTime(next)
			w.NextExpected = &t
		}
	}
	return w
}

// CanCompleteInWindow answers whether a transfer fits inside a typical contact.
//
// Being briefly reachable is not the same as being rotatable. A node that
// surfaces for thirty seconds a day cannot receive a credential that takes two
// minutes to transfer, and it will fail identically every day forever unless
// somebody notices the arithmetic.
func CanCompleteInWindow(w *fleetv1.ContactWindow, need time.Duration) (bool, string) {
	if w == nil || w.TypicalDuration == nil {
		return true, "" // unknown; do not refuse on missing data
	}
	if w.TypicalDuration.Duration >= need {
		return true, ""
	}
	return false, fmt.Sprintf(
		"contact lasts ~%s, transfer needs ~%s — will never complete on this transport",
		shortDuration(w.TypicalDuration.Duration), shortDuration(need))
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 20; i++ {
		z = (z + x/z) / 2
	}
	return z
}
