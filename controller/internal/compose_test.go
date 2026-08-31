package internal

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

func TestApplyWindowBlocks(t *testing.T) {
	ro := &fleetv1.FirmwareRollout{}
	next := time.Now().Add(6 * time.Hour)
	proceed, budget := ApplyWindow(ro, WindowDecision{
		Open: false, Window: "field-sensors-nightly",
		Reason: "holiday change freeze", NextOpen: &next,
	}, time.Now())

	t.Logf("phase=%s blockedBy=%s reason=%q proceed=%v",
		ro.Status.Phase, ro.Status.BlockedBy, ro.Status.BlockReason, proceed)
	if proceed { t.Error("must not proceed while blocked") }
	if ro.Status.Phase != fleetv1.PhaseBlocked {
		t.Errorf("phase=%s want Blocked", ro.Status.Phase)
	}
	if ro.Status.NextWindow == nil { t.Error("NextWindow must be set so the answer is in the object") }
	_ = budget
}

func TestApplyWindowBudget(t *testing.T) {
	ro := &fleetv1.FirmwareRollout{}
	ro.Status.DispatchedThisWindow = 8
	now := metav1.Now()
	ro.Status.WindowOpenedAt = &now

	proceed, budget := ApplyWindow(ro, WindowDecision{
		Open: true, Window: "nightly", MaxDevices: 10,
	}, time.Now())
	t.Logf("dispatched=8 limit=10 -> proceed=%v budget=%d", proceed, budget)
	if !proceed || budget != 2 { t.Errorf("proceed=%v budget=%d, want true/2", proceed, budget) }

	ro.Status.DispatchedThisWindow = 10
	proceed, budget = ApplyWindow(ro, WindowDecision{
		Open: true, Window: "nightly", MaxDevices: 10,
	}, time.Now())
	t.Logf("dispatched=10 limit=10 -> proceed=%v phase=%s", proceed, ro.Status.Phase)
	if proceed { t.Error("must stop once the blast-radius limit is reached") }
	if ro.Status.Phase != fleetv1.PhaseWaiting {
		t.Errorf("phase=%s want Waiting", ro.Status.Phase)
	}
}
