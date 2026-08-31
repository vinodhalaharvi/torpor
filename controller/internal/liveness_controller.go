package internal

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

const (
	defaultExpectedInterval = 60 * time.Second
	defaultStaleMultiplier  = 3
)

var (
	deviceGVK = schema.GroupVersionKind{
		Group: "devices.kubeedge.io", Version: "v1beta1", Kind: "Device",
	}
	deviceStatusGVK = schema.GroupVersionKind{
		Group: "devices.kubeedge.io", Version: "v1beta1", Kind: "DeviceStatus",
	}
)

// LivenessReconciler derives a liveness state for every Device.
//
// It reads timestamps and writes an opinion. That is all it does, and the
// smallness is the point: every V3 concept — health gates, pending-until-in-
// range, refusing a rollout — asks this same question, and none of them should
// answer it inline.
type LivenessReconciler struct {
	client.Client
}

func (r *LivenessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)

	dev := &unstructured.Unstructured{}
	dev.SetGroupVersionKind(deviceGVK)
	if err := r.Get(ctx, req.NamespacedName, dev); err != nil {
		if errors.IsNotFound(err) {
			// Device gone. The DeviceLiveness owns nothing and is owned by the
			// Device, so garbage collection removes it.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	live := &fleetv1.DeviceLiveness{}
	err := r.Get(ctx, req.NamespacedName, live)
	switch {
	case errors.IsNotFound(err):
		live = &fleetv1.DeviceLiveness{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.Name,
				Namespace: req.Namespace,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: deviceGVK.GroupVersion().String(),
					Kind:       deviceGVK.Kind,
					Name:       dev.GetName(),
					UID:        dev.GetUID(),
				}},
			},
			Spec: fleetv1.DeviceLivenessSpec{DeviceRef: req.Name},
		}
		if err := r.Create(ctx, live); err != nil {
			return ctrl.Result{}, err
		}
	case err != nil:
		return ctrl.Result{}, err
	}

	interval, mult := r.thresholds(live, dev)
	status := r.assess(ctx, req.NamespacedName, dev, live, interval, mult)

	live.Status = status
	live.Status.ObservedGeneration = live.Generation
	if err := r.Status().Update(ctx, live); err != nil {
		return ctrl.Result{}, err
	}
	lg.V(1).Info("liveness", "device", req.Name,
		"state", status.State, "silent", status.SilentFor)

	// Requeue at a fraction of the interval so a transition to Sleeping is
	// noticed promptly rather than at the next unrelated event. A device that
	// speaks daily does not need to be polled every ten seconds — the requeue
	// scales with the device's own rhythm, which is the whole idea.
	requeue := interval / 3
	if requeue < 5*time.Second {
		requeue = 5 * time.Second
	}
	if requeue > 5*time.Minute {
		requeue = 5 * time.Minute
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// thresholds resolves what counts as normal silence for this device.
//
// Precedence: the DeviceLiveness spec, then the Device's own protocol config
// (where the mapper already reads it from), then a default. The middle case
// matters — expectedIntervalSeconds is a property of how the device was
// deployed, and repeating it in two places would let them disagree.
func (r *LivenessReconciler) thresholds(
	live *fleetv1.DeviceLiveness, dev *unstructured.Unstructured,
) (time.Duration, int32) {
	interval := live.Spec.ExpectedInterval.Duration
	mult := live.Spec.StaleMultiplier

	cfg, _, _ := unstructured.NestedMap(dev.Object, "spec", "protocol", "configData")
	if interval == 0 {
		if v, ok := cfg["expectedIntervalSeconds"]; ok {
			if secs, err := toInt64(v); err == nil && secs > 0 {
				interval = time.Duration(secs) * time.Second
			}
		}
	}
	if mult == 0 {
		if v, ok := cfg["staleMultiplier"]; ok {
			if m, err := toInt64(v); err == nil && m > 0 {
				mult = int32(m)
			}
		}
	}

	if interval == 0 {
		interval = defaultExpectedInterval
	}
	if mult == 0 {
		mult = defaultStaleMultiplier
	}
	return interval, mult
}

// assess is the actual judgement, and it is four lines of arithmetic wrapped in
// a lot of care about what the numbers mean.
func (r *LivenessReconciler) assess(
	ctx context.Context, key types.NamespacedName,
	dev *unstructured.Unstructured, live *fleetv1.DeviceLiveness,
	interval time.Duration, mult int32,
) fleetv1.DeviceLivenessStatus {
	out := fleetv1.DeviceLivenessStatus{
		Transport: dev.GetLabels()["transport"],
	}
	if out.Transport == "" {
		out.Transport = "wifi"
	}
	cfg, _, _ := unstructured.NestedMap(dev.Object, "spec", "protocol", "configData")
	if gw, ok := cfg["gateway"].(string); ok {
		out.Gateway = gw
	}

	last, ok := r.lastReport(ctx, key, live.Spec.Property)
	if !ok {
		out.State = fleetv1.StateUnknown
		out.Assessment = "NeverReported"
		return out
	}

	out.LastSeen = &metav1.Time{Time: last}
	silent := time.Since(last)
	out.SilentFor = shortDuration(silent)
	next := last.Add(interval)
	out.NextExpectedBy = &metav1.Time{Time: next}

	switch {
	case silent <= interval:
		out.State = fleetv1.StateOnline
		out.Assessment = "ReportingOnSchedule"

	case silent <= interval*time.Duration(mult):
		// The row Kubernetes cannot produce. Silent, expected to be silent,
		// and entirely healthy. A Node in this condition would be NotReady and
		// its pods would be evicted; this device is doing exactly what it was
		// deployed to do.
		out.State = fleetv1.StateSleeping
		out.Assessment = "WithinExpectedWakeInterval"

	default:
		// Note what this does not say: Failed. The device may be fine and its
		// gateway down, or fine and out of range. Unreachable is a statement
		// about our knowledge, not about the hardware.
		out.State = fleetv1.StateUnreachable
		out.Assessment = fmt.Sprintf("NoReportFor%s", shortDuration(silent))
	}
	return out
}

// lastReport finds the freshest twin timestamp on the Device's status object.
//
// KubeEdge stamps each reported value in milliseconds since epoch. Taking the
// max across properties rather than a specific one means any evidence of life
// counts, which is the right default — a node that reports humidity but not
// temperature is still alive.
func (r *LivenessReconciler) lastReport(
	ctx context.Context, key types.NamespacedName, only string,
) (time.Time, bool) {
	ds := &unstructured.Unstructured{}
	ds.SetGroupVersionKind(deviceStatusGVK)
	if err := r.Get(ctx, key, ds); err != nil {
		return time.Time{}, false
	}
	twins, _, _ := unstructured.NestedSlice(ds.Object, "status", "twins")

	var newest time.Time
	for _, t := range twins {
		tw, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if only != "" {
			if name, _, _ := unstructured.NestedString(tw, "propertyName"); name != only {
				continue
			}
		}
		raw, found, _ := unstructured.NestedString(tw, "reported", "metadata", "timestamp")
		if !found {
			continue
		}
		ms, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		if ts := time.UnixMilli(ms); ts.After(newest) {
			newest = ts
		}
	}
	return newest, !newest.IsZero()
}

func toInt64(v interface{}) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case string:
		return strconv.ParseInt(n, 10, 64)
	}
	return 0, fmt.Errorf("not a number: %T", v)
}

// shortDuration renders 3h12m rather than 3h12m4.28s. Operators read this
// column at a glance; sub-second precision is noise at every timescale that
// matters here.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func (r *LivenessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	dev := &unstructured.Unstructured{}
	dev.SetGroupVersionKind(deviceGVK)

	// Watching Device rather than DeviceLiveness: the Device is the source of
	// truth and the liveness object is derived. Reconcile creates it if absent.
	return ctrl.NewControllerManagedBy(mgr).
		For(dev).
		Named("deviceliveness").
		Complete(r)
}
