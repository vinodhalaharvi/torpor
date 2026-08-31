package internal

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// How the objects compose.
//
// Nothing here references anything by name. A MaintenanceWindow does not know
// which rollouts it governs, and a FirmwareRollout does not know a window
// exists — they meet through labels on the Device, the way a NetworkPolicy and
// a Pod do.
//
// That matters organisationally rather than technically. The person who
// decides it is unsafe to touch a pump during the day shift is not the person
// shipping v42, and neither should have to read the other's YAML for the
// system to behave correctly.
//
// The decision chain for a single device, in this order:
//
//	1. Capability   can this device EVER take this?      -> Refused   permanent
//	2. Window       are we permitted to act right now?    -> Blocked   policy
//	3. Liveness     is it reachable?                      -> Pending   temporary
//	4. Contact      will contact last long enough?        -> Pending   arithmetic
//	5. Transport    which door — rechecked at write time
//
// The order is not arbitrary and it is not an optimisation. It determines what
// an operator is told, and the wrong order sends them somewhere useless.
//
// Capability first, because "never" beats "not now". Window before liveness,
// because a device that is awake during a change freeze is still off limits —
// reporting that as "pending, device asleep" sends somebody to debug a radio
// when the real answer is "frozen until January".

// WindowDecision is the aggregate verdict of every MaintenanceWindow that
// selects a given fleet.
type WindowDecision struct {
	Open     bool
	Window   string
	Reason   string
	NextOpen *time.Time
	MaxDevices int32
}

// EvaluateWindowsFor finds every MaintenanceWindow selecting any device in the
// rollout's fleet, and takes the most restrictive answer.
//
// Most restrictive rather than first match: overlapping policies are normal —
// a site-wide nightly window and a company-wide holiday freeze — and if any of
// them says no, the answer is no. A policy that could be escaped by adding
// another policy is not a policy.
func EvaluateWindowsFor(
	ctx context.Context, c client.Client,
	namespace string, deviceLabels []labels.Set, now time.Time,
) (WindowDecision, error) {
	var list fleetv1.MaintenanceWindowList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return WindowDecision{Open: true}, err
	}
	// No windows configured means always permitted. Right for a bench, and the
	// reason this object is opt-in rather than defaulted.
	if len(list.Items) == 0 {
		return WindowDecision{Open: true, Reason: "no maintenance window configured"}, nil
	}

	decision := WindowDecision{Open: true, Reason: "no window selects these devices"}
	var earliestOpen *time.Time

	for i := range list.Items {
		mw := &list.Items[i]
		if !windowSelectsAny(mw, deviceLabels) {
			continue
		}
		st := EvaluateWindow(&mw.Spec, now)

		if st.Open {
			if decision.Open {
				decision = WindowDecision{
					Open:       true,
					Window:     mw.Name,
					Reason:     st.Reason,
					MaxDevices: mw.Spec.MaxDevicesPerWindow,
				}
			}
			continue
		}

		// A closed window wins over any open one.
		if st.NextOpen != nil && (earliestOpen == nil || st.NextOpen.After(*earliestOpen)) {
			// Latest of the blocking windows: if two policies block, we are
			// free only when the last of them releases.
			earliestOpen = st.NextOpen
		}
		decision = WindowDecision{
			Open:     false,
			Window:   mw.Name,
			Reason:   st.Reason,
			NextOpen: earliestOpen,
		}
	}
	return decision, nil
}

func windowSelectsAny(mw *fleetv1.MaintenanceWindow, deviceLabels []labels.Set) bool {
	if mw.Spec.Selector == nil {
		return true // no selector means everything
	}
	sel, err := metav1.LabelSelectorAsSelector(mw.Spec.Selector)
	if err != nil {
		return false
	}
	for _, l := range deviceLabels {
		if sel.Matches(l) {
			return true
		}
	}
	return false
}

// ApplyWindow folds a window decision into a rollout's status and reports
// whether dispatch may proceed, and how many devices it may touch.
func ApplyWindow(
	ro *fleetv1.FirmwareRollout, d WindowDecision, now time.Time,
) (proceed bool, budget int32) {
	if !d.Open {
		ro.Status.Phase = fleetv1.PhaseBlocked
		ro.Status.BlockedBy = d.Window
		ro.Status.BlockReason = d.Reason
		if d.NextOpen != nil {
			t := metav1.NewTime(*d.NextOpen)
			ro.Status.NextWindow = &t
		}
		setCondition(ro, "Progressing", metav1.ConditionFalse, "MaintenanceWindowClosed",
			fmt.Sprintf("%s: %s", d.Window, d.Reason))
		return false, 0
	}

	ro.Status.BlockedBy = ""
	ro.Status.BlockReason = ""
	ro.Status.NextWindow = nil

	// A new window resets the per-window budget. The limit is a blast radius
	// per opportunity to notice something has gone wrong, not a lifetime cap.
	if ro.Status.WindowOpenedAt == nil ||
		(d.Window != "" && ro.Status.BlockedBy != d.Window && ro.Status.DispatchedThisWindow > 0 &&
			ro.Status.WindowOpenedAt.Time.Before(now.Add(-time.Hour))) {
		t := metav1.NewTime(now)
		ro.Status.WindowOpenedAt = &t
		ro.Status.DispatchedThisWindow = 0
	}

	if d.MaxDevices > 0 {
		remaining := d.MaxDevices - ro.Status.DispatchedThisWindow
		if remaining <= 0 {
			ro.Status.Phase = fleetv1.PhaseWaiting
			setCondition(ro, "Progressing", metav1.ConditionFalse, "WindowBudgetExhausted",
				fmt.Sprintf("%d devices already dispatched this window (limit %d)",
					ro.Status.DispatchedThisWindow, d.MaxDevices))
			return false, 0
		}
		return true, remaining
	}
	return true, -1 // unlimited
}

// DeviceLabelsFor collects the label sets of every device a rollout selects, so
// windows can be matched without the rollout knowing which windows exist.
func DeviceLabelsFor(
	ctx context.Context, c client.Client, ro *fleetv1.FirmwareRollout,
) ([]labels.Set, error) {
	sel, err := metav1.LabelSelectorAsSelector(ro.Spec.Selector)
	if err != nil {
		return nil, err
	}
	devs := &unstructured.UnstructuredList{}
	devs.SetGroupVersionKind(deviceGVK)
	if err := c.List(ctx, devs, client.InNamespace(ro.Namespace)); err != nil {
		return nil, err
	}
	var out []labels.Set
	for _, d := range devs.Items {
		l := labels.Set(d.GetLabels())
		if sel.Matches(l) {
			out = append(out, l)
		}
	}
	return out, nil
}

var _ = ctrl.Result{}
