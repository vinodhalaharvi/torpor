package internal

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

func at(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func TestMaintenanceWindow(t *testing.T) {
	spec := &fleetv1.MaintenanceWindowSpec{
		Timezone: "UTC",
		Allow: []fleetv1.WindowPeriod{{
			Cron:     "0 2 * * *",
			Duration: &metav1.Duration{Duration: 2 * time.Hour},
			Reason:   "nightly maintenance",
		}},
		Deny: []fleetv1.WindowPeriod{{
			From:   &metav1.Time{Time: at("2026-12-20T00:00:00Z")},
			Until:  &metav1.Time{Time: at("2027-01-05T00:00:00Z")},
			Reason: "holiday change freeze",
		}},
	}
	for _, c := range []struct {
		when string; open bool; note string
	}{
		{"2026-08-30T02:30:00Z", true,  "inside nightly window"},
		{"2026-08-30T04:30:00Z", false, "after it closes"},
		{"2026-08-30T14:00:00Z", false, "middle of the day"},
		{"2026-12-25T02:30:00Z", false, "inside window but frozen"},
	} {
		got := EvaluateWindow(spec, at(c.when))
		t.Logf("%-22s open=%-5v  %-24s (%s)", c.when, got.Open, got.Reason, c.note)
		if got.Open != c.open {
			t.Errorf("%s: open=%v want %v", c.when, got.Open, c.open)
		}
	}
}

func TestContactWindowConfidence(t *testing.T) {
	base := at("2026-08-30T00:00:00Z")
	regular := []time.Time{}
	for i := 0; i < 12; i++ {
		regular = append(regular, base.Add(time.Duration(i)*4*time.Hour))
	}
	erratic := []time.Time{base, base.Add(1 * time.Hour), base.Add(9 * time.Hour), base.Add(10 * time.Hour)}

	for _, c := range []struct {
		name string; contacts []time.Time; want string
	}{
		{"regular 4h x12", regular, fleetv1.ConfidenceHigh},
		{"erratic x4", erratic, fleetv1.ConfidenceLow},
		{"single", []time.Time{base}, fleetv1.ConfidenceNone},
	} {
		w := LearnContactWindow("wifi", c.contacts, base.Add(50*time.Hour))
		iv := "-"
		if w.TypicalInterval != nil { iv = shortDuration(w.TypicalInterval.Duration) }
		t.Logf("%-16s samples=%-3d interval=%-8s confidence=%s",
			c.name, w.SamplesObserved, iv, w.Confidence)
		if w.Confidence != c.want {
			t.Errorf("%s: confidence=%s want %s", c.name, w.Confidence, c.want)
		}
	}
}

func TestCanCompleteInWindow(t *testing.T) {
	w := &fleetv1.ContactWindow{
		Transport:       "wifi",
		TypicalDuration: &metav1.Duration{Duration: 30 * time.Second},
	}
	ok, why := CanCompleteInWindow(w, 2*time.Minute)
	t.Logf("30s contact, 2m transfer -> ok=%v %s", ok, why)
	if ok { t.Error("a 2m transfer must not fit a 30s contact") }
}
