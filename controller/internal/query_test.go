package internal

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

func ld(name string, state fleetv1.LivenessState, hash string, ts []fleetv1.Transport,
	via []string, batt int32) fleetv1.DeviceLiveness {
	seen := metav1.NewTime(time.Now().Add(-time.Minute))
	return fleetv1.DeviceLiveness{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: fleetv1.DeviceLivenessStatus{
			State: state, LastSeen: &seen, SilentFor: "1m",
			Transport: ts[0].Type,
			Capability: &fleetv1.DeviceCapability{
				Transports: ts, ReachableVia: via,
				RunningConfigHash: hash, BatteryPercent: batt,
			},
		},
	}
}

func b(v bool) *bool { return &v }
func i32(v int32) *int32 { return &v }

func fleet() []fleetv1.DeviceLiveness {
	wifi := fleetv1.Transport{Type: "wifi", OTA: true, Config: true}
	lora := fleetv1.Transport{Type: "lora", OTA: false, Config: true}
	return []fleetv1.DeviceLiveness{
		ld("w10-a", fleetv1.StateOnline, "v42", []fleetv1.Transport{wifi}, []string{"wifi"}, 100),
		ld("w10-b", fleetv1.StateOnline, "v40", []fleetv1.Transport{wifi}, []string{"wifi"}, 100),
		ld("field-01", fleetv1.StateSleeping, "v40", []fleetv1.Transport{lora}, []string{"lora"}, 40),
		ld("field-19", fleetv1.StateOnline, "v40", []fleetv1.Transport{lora, wifi}, []string{"lora"}, 15),
		ld("field-33", fleetv1.StateUnreachable, "v40", []fleetv1.Transport{lora}, []string{}, 8),
	}
}

func TestQueryDriftedAndFixable(t *testing.T) {
	// "everything not on v42 that I can actually update right now"
	st := EvaluateQuery(&fleetv1.FleetQuerySpec{
		Where: &fleetv1.QueryPredicate{
			RunningConfigHash: &fleetv1.StringMatch{NotIn: []string{"v42"}},
			OTAReachable:      b(true),
		},
	}, fleet(), time.Now())

	t.Logf("%s", QuerySummaryLine(&st))
	for _, d := range st.Devices {
		t.Logf("  %-9s %-12s hash=%-4s actionable=%v %s",
			d.Device, d.State, d.RunningConfigHash, d.Actionable, d.Reason)
	}
	if st.Matched != 1 || st.Devices[0].Device != "w10-b" {
		t.Errorf("matched=%d want just w10-b", st.Matched)
	}
}

func TestQueryDriftedTotal(t *testing.T) {
	// "everything not on v42", regardless of whether it can be fixed
	st := EvaluateQuery(&fleetv1.FleetQuerySpec{
		Where: &fleetv1.QueryPredicate{
			RunningConfigHash: &fleetv1.StringMatch{NotIn: []string{"v42"}},
		},
	}, fleet(), time.Now())

	t.Logf("%s", QuerySummaryLine(&st))
	for _, d := range st.Devices {
		t.Logf("  %-9s actionable=%-5v %s", d.Device, d.Actionable, d.Reason)
	}
	if st.Matched != 4 { t.Errorf("matched=%d want 4", st.Matched) }
	if st.Summary["actionable"] != 1 {
		t.Errorf("actionable=%d want 1", st.Summary["actionable"])
	}
}

func TestQueryLowBatteryNeedingVisit(t *testing.T) {
	// "who is low on battery and cannot be helped remotely" — a truck schedule
	st := EvaluateQuery(&fleetv1.FleetQuerySpec{
		Where: &fleetv1.QueryPredicate{
			BatteryBelowPercent: i32(20),
			OTACapable:          b(false),
		},
	}, fleet(), time.Now())

	t.Logf("%s", QuerySummaryLine(&st))
	for _, d := range st.Devices {
		t.Logf("  %-9s %-12s %s", d.Device, d.State, d.Reason)
	}
	if st.Matched != 1 || st.Devices[0].Device != "field-33" {
		t.Errorf("matched=%d want just field-33", st.Matched)
	}
}
