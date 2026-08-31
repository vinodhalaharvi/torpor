package internal

import (
	"context"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// Property names the rollout drives on the device. These are ordinary Device
// properties — the rollout writes desired state and reads reported state, the
// same mechanism as every other property in this project. A firmware update is
// not a special transport, it is a property whose value happens to be a URL.
const (
	propFirmwareURL  = "firmware_url"
	propRunningHash  = "running_config_hash"
	propBootCount    = "boot_count"
	rolloutHashLabel = "fleet.torpor.io/config-hash"
)

type RolloutReconciler struct {
	client.Client
}

func (r *RolloutReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)

	ro := &fleetv1.FirmwareRollout{}
	if err := r.Get(ctx, req.NamespacedName, ro); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if ro.Status.Phase == fleetv1.PhaseComplete ||
		ro.Status.Phase == fleetv1.PhaseRolledBack {
		return ctrl.Result{}, nil
	}

	// --- plan ------------------------------------------------------------
	//
	// Replanned every reconcile, not once at the start. Capability is
	// time-varying: a node that was Pending because its only live transport
	// was LoRa becomes Eligible when it drifts into WiFi range, with nothing
	// about the rollout or the device spec having changed.
	livenesses, err := r.matchingLivenesses(ctx, ro)
	if err != nil {
		return ctrl.Result{}, err
	}
	plan := PlanRollout(ro, livenesses)

	ro.Status.Target = int32(len(livenesses))
	ro.Status.Eligible = int32(len(plan.Eligible))
	ro.Status.Refused = plan.Refused
	ro.Status.Pending = plan.Pending
	ro.Status.ObservedGeneration = ro.Generation
	if ro.Status.StartedAt == nil {
		now := metav1.Now()
		ro.Status.StartedAt = &now
	}

	if ro.Spec.Paused {
		ro.Status.Phase = fleetv1.PhasePaused
		return r.finish(ctx, ro, 30*time.Second)
	}

	// --- observe ---------------------------------------------------------
	updated, healthy, failed := r.observe(ctx, ro, plan.Eligible)
	ro.Status.Updated = int32(len(updated))
	ro.Status.Healthy = int32(len(healthy))
	ro.Status.Failed = failed

	// --- stop conditions -------------------------------------------------
	if tol := failureTolerance(ro); len(failed) >= tol {
		ro.Status.Phase = fleetv1.PhasePaused
		setCondition(ro, "Progressing", metav1.ConditionFalse,
			fleetv1.ReasonHealthGateFail,
			fmt.Sprintf("%d device(s) failed, tolerance %d", len(failed), tol))
		lg.Info("rollout paused", "rollout", ro.Name, "failed", len(failed))
		return r.finish(ctx, ro, time.Minute)
	}

	if len(plan.Eligible) == 0 {
		// Nothing is wrong and nothing can proceed. A rollout may sit here for
		// days and be entirely on track — which is a sentence no other tool
		// can say about itself.
		ro.Status.Phase = fleetv1.PhaseWaiting
		setCondition(ro, "Progressing", metav1.ConditionFalse, "NoEligibleDevices",
			fmt.Sprintf("%d refused, %d pending", len(plan.Refused), len(plan.Pending)))
		return r.finish(ctx, ro, time.Minute)
	}

	if len(healthy) >= len(plan.Eligible) {
		ro.Status.Phase = fleetv1.PhaseComplete
		ro.Status.Step = "done"
		setCondition(ro, "Complete", metav1.ConditionTrue, "AllEligibleHealthy",
			fmt.Sprintf("%d/%d healthy, %d refused", len(healthy),
				len(plan.Eligible), len(plan.Refused)))
		return r.finish(ctx, ro, 0)
	}

	// --- advance ---------------------------------------------------------
	steps := ro.Spec.Strategy.Steps
	if len(steps) == 0 {
		steps = []int32{100}
	}
	stepIdx, allowed := currentStep(ro, steps, len(plan.Eligible), len(healthy))
	ro.Status.Step = fmt.Sprintf("%d/%d", stepIdx+1, len(steps))
	if stepIdx == 0 && ro.Spec.Strategy.Canary > 0 {
		ro.Status.Phase = fleetv1.PhaseCanary
	} else {
		ro.Status.Phase = fleetv1.PhaseProgressing
	}

	// Wait for the current step to settle before opening the next. Without
	// this the whole fleet updates in one reconcile and the canary is
	// decorative.
	if len(updated) >= allowed {
		setCondition(ro, "Progressing", metav1.ConditionTrue, "AwaitingHealth",
			fmt.Sprintf("%d updated, %d healthy, step allows %d",
				len(updated), len(healthy), allowed))
		return r.finish(ctx, ro, 15*time.Second)
	}

	// Concurrency is bounded by the medium, not the CPU. On LoRa or a shared
	// RS-485 segment, exceeding this produces a schedule that is illegal
	// rather than merely slow.
	budget := allowed - len(updated)
	if mc := int(ro.Spec.Strategy.MaxConcurrent); mc > 0 && budget > mc {
		budget = mc
	}

	for _, dev := range nextDevices(plan.Eligible, updated, budget) {
		if err := r.dispatch(ctx, ro, dev); err != nil {
			lg.Error(err, "dispatch failed", "device", dev)
			continue
		}
		lg.Info("dispatched", "rollout", ro.Name, "device", dev,
			"hash", ro.Spec.Source.ConfigHash)
	}
	return r.finish(ctx, ro, 15*time.Second)
}

// dispatch writes the firmware URL to the device as ordinary desired state.
//
// The device pulls the image itself — ESPHome's http_request OTA platform —
// rather than being pushed to. That inversion is what makes a fleet update
// possible at all: a push requires the controller to reach every device
// simultaneously, and half of them are asleep.
func (r *RolloutReconciler) dispatch(
	ctx context.Context, ro *fleetv1.FirmwareRollout, device string,
) error {
	dev := &unstructured.Unstructured{}
	dev.SetGroupVersionKind(deviceGVK)
	key := types.NamespacedName{Name: device, Namespace: ro.Namespace}
	if err := r.Get(ctx, key, dev); err != nil {
		return err
	}

	props, _, err := unstructured.NestedSlice(dev.Object, "spec", "properties")
	if err != nil {
		return err
	}
	// Value carries the hash alongside the URL so the device can report back
	// what it believes it installed, without a second round trip.
	value := fmt.Sprintf("%s|%s", ro.Spec.Source.ConfigHash, ro.Spec.Source.Package)

	found := false
	for i, p := range props {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(pm, "name"); name != propFirmwareURL {
			continue
		}
		if err := unstructured.SetNestedField(pm, value, "desired", "value"); err != nil {
			return err
		}
		props[i] = pm
		found = true
		break
	}
	if !found {
		return fmt.Errorf("device %s has no %q property; add it to the DeviceModel",
			device, propFirmwareURL)
	}
	if err := unstructured.SetNestedSlice(dev.Object, props, "spec", "properties"); err != nil {
		return err
	}
	return r.Update(ctx, dev)
}

// observe reads back what actually happened.
//
// Health is measured against the hash the device reports it is RUNNING, not
// against what it was told to run. A device can acknowledge an instruction it
// never carried out — that failure has already occurred in this project, with
// a converged twin, a clean broker round trip, and hardware that did nothing.
func (r *RolloutReconciler) observe(
	ctx context.Context, ro *fleetv1.FirmwareRollout, eligible []string,
) (updated, healthy []string, failed []fleetv1.DeviceOutcome) {
	gate := ro.Spec.Strategy.HealthGate
	want := ro.Spec.Source.ConfigHash

	for _, name := range eligible {
		key := types.NamespacedName{Name: name, Namespace: ro.Namespace}

		desired := r.desiredValue(ctx, key, propFirmwareURL)
		if desired == "" {
			continue // not dispatched yet
		}
		updated = append(updated, name)

		running := r.reportedValue(ctx, key, propRunningHash)
		if running == want {
			// Boot loop check. A device that boots, reports, and then boot
			// loops would pass a single check taken immediately after the
			// update — which is what settleFor exists to prevent.
			if gate != nil && ro.Spec.Strategy.RollbackOn != nil &&
				ro.Spec.Strategy.RollbackOn.BootLoopThreshold > 0 {
				if boots := r.reportedInt(ctx, key, propBootCount); boots >
					int64(ro.Spec.Strategy.RollbackOn.BootLoopThreshold) {
					failed = append(failed, fleetv1.DeviceOutcome{
						Device: name, Reason: fleetv1.ReasonBootLoop,
						Detail: fmt.Sprintf("%d boots since update", boots),
					})
					continue
				}
			}
			healthy = append(healthy, name)
			continue
		}

		// Not yet running the new hash. Whether that is a failure depends
		// entirely on how long it has been, which is the health gate's job.
		if gate != nil && gate.MustReportWithin != nil {
			since := r.dispatchedAt(ctx, key)
			if !since.IsZero() && time.Since(since) > gate.MustReportWithin.Duration {
				failed = append(failed, fleetv1.DeviceOutcome{
					Device: name, Reason: fleetv1.ReasonHealthGateFail,
					Detail: fmt.Sprintf("did not report hash %s within %s",
						want, gate.MustReportWithin.Duration),
					Since: &metav1.Time{Time: since},
				})
			}
		}
	}
	return updated, healthy, failed
}

func (r *RolloutReconciler) matchingLivenesses(
	ctx context.Context, ro *fleetv1.FirmwareRollout,
) ([]fleetv1.DeviceLiveness, error) {
	sel, err := metav1.LabelSelectorAsSelector(ro.Spec.Selector)
	if err != nil {
		return nil, err
	}

	devs := &unstructured.UnstructuredList{}
	devs.SetGroupVersionKind(deviceGVK)
	devs.GetObjectKind().SetGroupVersionKind(deviceGVK)
	if err := r.List(ctx, devs, client.InNamespace(ro.Namespace)); err != nil {
		return nil, err
	}

	var out []fleetv1.DeviceLiveness
	for _, d := range devs.Items {
		if !sel.Matches(labels.Set(d.GetLabels())) {
			continue
		}
		l := fleetv1.DeviceLiveness{}
		err := r.Get(ctx, types.NamespacedName{
			Name: d.GetName(), Namespace: d.GetNamespace()}, &l)
		if errors.IsNotFound(err) {
			// No liveness yet. Treat as Unknown rather than skipping — a
			// device the planner cannot assess is Pending, not absent.
			l.Name = d.GetName()
			l.Namespace = d.GetNamespace()
			l.Status.State = fleetv1.StateUnknown
		} else if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	// Stable order so canary selection is deterministic across reconciles —
	// otherwise the "one device" in step 1 could be a different device each
	// time, which defeats the purpose of watching it.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *RolloutReconciler) property(
	ctx context.Context, key types.NamespacedName, name, field string,
) string {
	if field == "desired" {
		dev := &unstructured.Unstructured{}
		dev.SetGroupVersionKind(deviceGVK)
		if err := r.Get(ctx, key, dev); err != nil {
			return ""
		}
		props, _, _ := unstructured.NestedSlice(dev.Object, "spec", "properties")
		for _, p := range props {
			pm, _ := p.(map[string]interface{})
			if n, _, _ := unstructured.NestedString(pm, "name"); n == name {
				v, _, _ := unstructured.NestedString(pm, "desired", "value")
				return v
			}
		}
		return ""
	}

	ds := &unstructured.Unstructured{}
	ds.SetGroupVersionKind(deviceStatusGVK)
	if err := r.Get(ctx, key, ds); err != nil {
		return ""
	}
	twins, _, _ := unstructured.NestedSlice(ds.Object, "status", "twins")
	for _, t := range twins {
		tw, _ := t.(map[string]interface{})
		if n, _, _ := unstructured.NestedString(tw, "propertyName"); n == name {
			v, _, _ := unstructured.NestedString(tw, "reported", "value")
			return v
		}
	}
	return ""
}

func (r *RolloutReconciler) desiredValue(ctx context.Context, k types.NamespacedName, n string) string {
	return r.property(ctx, k, n, "desired")
}
func (r *RolloutReconciler) reportedValue(ctx context.Context, k types.NamespacedName, n string) string {
	return r.property(ctx, k, n, "reported")
}
func (r *RolloutReconciler) reportedInt(ctx context.Context, k types.NamespacedName, n string) int64 {
	var v int64
	fmt.Sscanf(r.reportedValue(ctx, k, n), "%d", &v)
	return v
}

// dispatchedAt reads when the firmware property was last written. KubeEdge
// stamps reported values but not desired ones, so this uses the twin's own
// timestamp as a lower bound.
func (r *RolloutReconciler) dispatchedAt(ctx context.Context, k types.NamespacedName) time.Time {
	ds := &unstructured.Unstructured{}
	ds.SetGroupVersionKind(deviceStatusGVK)
	if err := r.Get(ctx, k, ds); err != nil {
		return time.Time{}
	}
	twins, _, _ := unstructured.NestedSlice(ds.Object, "status", "twins")
	for _, t := range twins {
		tw, _ := t.(map[string]interface{})
		if n, _, _ := unstructured.NestedString(tw, "propertyName"); n != propFirmwareURL {
			continue
		}
		raw, found, _ := unstructured.NestedString(tw, "observedDesired", "metadata", "timestamp")
		if !found {
			return time.Time{}
		}
		var ms int64
		fmt.Sscanf(raw, "%d", &ms)
		return time.UnixMilli(ms)
	}
	return time.Time{}
}

// currentStep returns the step index and how many devices it permits.
//
// Percentages are of ELIGIBLE, not of target. Rolling to "50%" of a fleet
// where a third is refused should reach half of what can actually take it.
func currentStep(
	ro *fleetv1.FirmwareRollout, steps []int32, eligible, healthy int,
) (int, int) {
	if c := int(ro.Spec.Strategy.Canary); c > 0 && healthy < c {
		return 0, c
	}
	for i, pct := range steps {
		allowed := StepSize(eligible, pct)
		if healthy < allowed {
			return i, allowed
		}
	}
	return len(steps) - 1, eligible
}

func nextDevices(eligible, updated []string, budget int) []string {
	done := make(map[string]bool, len(updated))
	for _, u := range updated {
		done[u] = true
	}
	var out []string
	for _, e := range eligible {
		if len(out) >= budget {
			break
		}
		if !done[e] {
			out = append(out, e)
		}
	}
	return out
}

func failureTolerance(ro *fleetv1.FirmwareRollout) int {
	if rb := ro.Spec.Strategy.RollbackOn; rb != nil && rb.HealthGateFailures > 0 {
		return int(rb.HealthGateFailures)
	}
	// One. On hardware you cannot recreate, the right instinct is to stop
	// early and think.
	return 1
}

func setCondition(ro *fleetv1.FirmwareRollout, typ string,
	status metav1.ConditionStatus, reason, msg string) {
	cond := metav1.Condition{
		Type: typ, Status: status, Reason: reason, Message: msg,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: ro.Generation,
	}
	for i, c := range ro.Status.Conditions {
		if c.Type == typ {
			if c.Status == status && c.Reason == reason {
				return // no churn on unchanged conditions
			}
			ro.Status.Conditions[i] = cond
			return
		}
	}
	ro.Status.Conditions = append(ro.Status.Conditions, cond)
}

func (r *RolloutReconciler) finish(
	ctx context.Context, ro *fleetv1.FirmwareRollout, requeue time.Duration,
) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, ro); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *RolloutReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetv1.FirmwareRollout{}).
		Named("firmwarerollout").
		Complete(r)
}
