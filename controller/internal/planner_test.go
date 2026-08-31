package internal

import (
	"testing"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

func mk(name string, state fleetv1.LivenessState, ts []fleetv1.Transport, via []string) fleetv1.DeviceLiveness {
	return fleetv1.DeviceLiveness{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: fleetv1.DeviceLivenessStatus{
			State: state, SilentFor: "1s",
			Capability: &fleetv1.DeviceCapability{Transports: ts, ReachableVia: via},
		},
	}
}

func TestPlan(t *testing.T) {
	wifi := fleetv1.Transport{Type: "wifi", OTA: true, Config: true, Availability: "always"}
	lora := fleetv1.Transport{Type: "lora", OTA: false, Config: true, Availability: "always"}
	ro := &fleetv1.FirmwareRollout{Spec: fleetv1.FirmwareRolloutSpec{
		Requires: &fleetv1.RolloutRequirements{OTA: true},
	}}
	p := PlanRollout(ro, []fleetv1.DeviceLiveness{
		mk("w10-a", fleetv1.StateOnline, []fleetv1.Transport{wifi}, []string{"wifi"}),
		mk("field-01", fleetv1.StateOnline, []fleetv1.Transport{lora}, []string{"lora"}),
		mk("field-07", fleetv1.StateSleeping, []fleetv1.Transport{wifi}, []string{"wifi"}),
		mk("field-19", fleetv1.StateOnline, []fleetv1.Transport{lora, wifi}, []string{"lora"}),
	})
	t.Logf("eligible=%v", p.Eligible)
	for _, r := range p.Refused { t.Logf("REFUSED %s: %s — %s", r.Device, r.Reason, r.Detail) }
	for _, r := range p.Pending { t.Logf("PENDING %s: %s — %s", r.Device, r.Reason, r.Detail) }

	if len(p.Eligible) != 1 || p.Eligible[0] != "w10-a" { t.Errorf("eligible = %v, want [w10-a]", p.Eligible) }
	if len(p.Refused) != 1 || p.Refused[0].Device != "field-01" { t.Errorf("refused = %v", p.Refused) }
	if len(p.Pending) != 2 { t.Errorf("pending = %v, want 2", p.Pending) }
}

func TestStepSize(t *testing.T) {
	for _, c := range []struct{ elig int; pct int32; want int }{
		{31, 1, 1}, {31, 10, 3}, {31, 50, 15}, {31, 100, 31}, {2, 50, 1},
	} {
		if got := StepSize(c.elig, c.pct); got != c.want {
			t.Errorf("StepSize(%d,%d) = %d, want %d", c.elig, c.pct, got, c.want)
		}
	}
}
