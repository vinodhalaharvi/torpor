package internal

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

func decom(dev string, reason fleetv1.DecommissionReason, phase fleetv1.DecommissionPhase) fleetv1.DeviceDecommission {
	return fleetv1.DeviceDecommission{
		ObjectMeta: metav1.ObjectMeta{Name: dev + "-decom"},
		Spec:       fleetv1.DeviceDecommissionSpec{DeviceRef: dev, Reason: reason},
		Status:     fleetv1.DeviceDecommissionStatus{Phase: phase},
	}
}

func TestDecommissionExcludesFromQueries(t *testing.T) {
	wifi := fleetv1.Transport{Type: "wifi", OTA: true, Config: true}
	lora := fleetv1.Transport{Type: "lora", OTA: false, Config: true}
	fleet := []fleetv1.DeviceLiveness{
		ld("w10-a", fleetv1.StateOnline, "v42", []fleetv1.Transport{wifi}, []string{"wifi"}, 100),
		ld("field-01", fleetv1.StateUnreachable, "v40", []fleetv1.Transport{lora}, nil, 5),
		ld("field-02", fleetv1.StateUnreachable, "v40", []fleetv1.Transport{lora}, nil, 0),
	}

	before := EvaluateQuery(&fleetv1.FleetQuerySpec{
		Where: &fleetv1.QueryPredicate{
			RunningConfigHash: &fleetv1.StringMatch{NotIn: []string{"v42"}},
		},
	}, fleet, time.Now())
	t.Logf("before decommission: %s", QuerySummaryLine(&before))

	set := NewDecommissionSet([]fleetv1.DeviceDecommission{
		decom("field-01", fleetv1.ReasonStolen, fleetv1.DecomComplete),
		decom("field-02", fleetv1.ReasonFailed, fleetv1.DecomPending), // not yet applied
	})
	after := EvaluateQuery(&fleetv1.FleetQuerySpec{
		Where: &fleetv1.QueryPredicate{
			RunningConfigHash: &fleetv1.StringMatch{NotIn: []string{"v42"}},
		},
	}, set.Filter(fleet), time.Now())
	t.Logf("after  decommission: %s", QuerySummaryLine(&after))

	if before.Matched != 2 { t.Errorf("before=%d want 2", before.Matched) }
	if after.Matched != 1 {
		t.Errorf("after=%d want 1 — a stolen device must stop being counted", after.Matched)
	}
}

func TestDecommissionCapturesHistory(t *testing.T) {
	wifi := fleetv1.Transport{Type: "wifi", OTA: true, Config: true}
	l := ld("field-07", fleetv1.StateUnreachable, "v40", []fleetv1.Transport{wifi}, nil, 3)
	first := time.Now().Add(-4 * 365 * 24 * time.Hour)

	st := BuildDecommissionStatus(&fleetv1.DeviceDecommissionSpec{
		Reason: fleetv1.ReasonBatteryExpired,
	}, &l, &first, time.Now())

	t.Logf("phase=%s serviceLife=%s finalHash=%s credsRevoked=%v",
		st.Phase, st.ServiceLife, st.FinalConfigHash, st.CredentialsRevoked)
	if st.ServiceLife == "" { t.Error("service life must be captured before exclusion") }
	if st.FinalConfigHash != "v40" { t.Error("final config hash lost") }
	if !st.CredentialsRevoked { t.Error("credentials must revoke by default") }
}

func TestDecommissionBlockedByTemplate(t *testing.T) {
	b := CheckDecommissionBlockers("field-01", nil,
		map[string][]string{"north-ridge-sensors": {"field-01", "field-02"}})
	for _, x := range b { t.Logf("  blocker: %s", x) }
	if len(b) != 1 { t.Errorf("blockers=%d want 1", len(b)) }
}

func TestDecommissionSummary(t *testing.T) {
	s := DecommissionSummary([]fleetv1.DeviceDecommission{
		decom("a", fleetv1.ReasonFailed, fleetv1.DecomComplete),
		decom("b", fleetv1.ReasonFailed, fleetv1.DecomComplete),
		decom("c", fleetv1.ReasonStolen, fleetv1.DecomComplete),
		decom("d", fleetv1.ReasonStolen, fleetv1.DecomPending),
	})
	t.Logf("  %v", s)
	if s["Failed"] != 2 || s["Stolen"] != 1 {
		t.Errorf("summary=%v; pending must not count", s)
	}
}
