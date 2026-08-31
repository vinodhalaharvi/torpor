package internal

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

func dev(name string, state fleetv1.LivenessState, hash string, seenAgo time.Duration,
	ts []fleetv1.Transport) fleetv1.DeviceLiveness {
	seen := metav1.NewTime(time.Now().Add(-seenAgo))
	return fleetv1.DeviceLiveness{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: fleetv1.DeviceLivenessStatus{
			State: state, LastSeen: &seen,
			Capability: &fleetv1.DeviceCapability{
				Transports: ts, ReachableVia: []string{ts[0].Type},
				RunningConfigHash: hash,
			},
		},
	}
}

var (
	wifiT = fleetv1.Transport{Type: "wifi", OTA: true, Config: true, MaxPayloadBytes: 1 << 20}
	loraT = fleetv1.Transport{Type: "lora", OTA: false, Config: true, MaxPayloadBytes: 240}
)

func TestDrift(t *testing.T) {
	now := time.Now()
	st := AssessDrift(&fleetv1.FleetDriftSpec{
		Expect:      &fleetv1.DriftExpectation{ConfigHash: "v42"},
		GracePeriod: &metav1.Duration{Duration: time.Minute},
	}, []fleetv1.DeviceLiveness{
		dev("w10-a", fleetv1.StateOnline, "v42", 2*time.Second, []fleetv1.Transport{wifiT}),
		dev("w10-b", fleetv1.StateOnline, "v40", 2*time.Hour, []fleetv1.Transport{wifiT}),
		dev("field-01", fleetv1.StateSleeping, "v40", 72*time.Hour, []fleetv1.Transport{loraT}),
	}, now)

	t.Logf("total=%d converged=%d drifted=%d unknown=%d oldest=%s",
		st.Total, st.Converged, st.Drifted, st.Unknown, st.OldestDrift)
	for _, d := range st.Devices {
		t.Logf("  %-9s %s -> %s  age=%-7s remediable=%v  %s",
			d.Device, d.Expected, d.Actual, d.DriftAge, d.Remediable, d.Assessment)
	}
	if st.Converged != 1 || st.Drifted != 2 {
		t.Errorf("converged=%d drifted=%d, want 1/2", st.Converged, st.Drifted)
	}
	for _, d := range st.Devices {
		if d.Device == "field-01" && d.Remediable {
			t.Error("field-01 is lora-only; must not be remediable")
		}
	}
}

func TestCredentials(t *testing.T) {
	now := time.Now()
	st := AssessCredentials(&fleetv1.CredentialExpirySpec{
		WarnBefore:        &metav1.Duration{Duration: 90 * 24 * time.Hour},
		RotationSizeBytes: 2048,
	}, []fleetv1.DeviceLiveness{
		dev("w10-a", fleetv1.StateOnline, "v42", time.Second, []fleetv1.Transport{wifiT}),
		dev("field-01", fleetv1.StateSleeping, "v42", time.Hour, []fleetv1.Transport{loraT}),
		dev("field-09", fleetv1.StateOnline, "v42", time.Second, []fleetv1.Transport{wifiT}),
	}, map[string]time.Time{
		"w10-a":    now.Add(400 * 24 * time.Hour),
		"field-01": now.Add(60 * 24 * time.Hour),
		"field-09": now.Add(60 * 24 * time.Hour),
	}, now)

	t.Logf("healthy=%d expiring=%d atRisk=%d expired=%d",
		st.Healthy, st.Expiring, st.AtRisk, st.Expired)
	for _, c := range st.Devices {
		t.Logf("  %-9s %-9s left=%-7s via=%v  %s",
			c.Device, c.State, c.TimeLeft, c.RotatableVia, c.ActionRequired)
	}
	if st.AtRisk != 1 {
		t.Errorf("atRisk=%d, want 1 (field-01 cannot take 2KB over lora)", st.AtRisk)
	}
	if st.Expiring != 1 || st.Healthy != 1 {
		t.Errorf("expiring=%d healthy=%d, want 1/1", st.Expiring, st.Healthy)
	}
}
