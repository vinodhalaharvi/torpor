package internal

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

func ann(dev, model, gw string, node int, hash string) fleetv1.DeviceAnnouncement {
	now := metav1.Now()
	return fleetv1.DeviceAnnouncement{
		Device: dev, Model: model, Gateway: gw, NodeID: node,
		ConfigHash: hash, AnnouncedAt: &now, LastAnnouncedAt: &now,
	}
}

func TestEnrollmentGate(t *testing.T) {
	spec := &fleetv1.DeviceEnrollmentSpec{
		RequireTemplate: "north-ridge-sensors",
		AllowedModels:   []string{"heltec-v3", "meshnology-w10"},
	}
	tmpl := map[string]bool{"field-01": true, "field-02": true, "field-03": true}
	existing := map[string]bool{"field-03": true}
	decom := map[string]bool{"field-22": true}

	decisions := AssessEnrollment(spec, []fleetv1.DeviceAnnouncement{
		ann("field-01", "heltec-v3", "gw-north", 2, "v41"),
		ann("field-02", "heltec-v3", "gw-north", 2, "v41"),   // node id clash
		ann("field-03", "heltec-v3", "gw-north", 4, "v41"),   // already enrolled
		ann("field-99", "heltec-v3", "gw-north", 5, "v41"),   // not in template
		ann("rogue-01", "unknown-board", "gw-north", 6, "v41"),
		ann("field-22", "heltec-v3", "gw-north", 7, "v41"),   // decommissioned
	}, existing, decom, tmpl, time.Now())

	t.Logf("  %s", EnrollmentSummary(decisions))
	for _, d := range decisions {
		verdict := "PENDING"
		if d.Approve { verdict = "APPROVE" }
		if d.Reject { verdict = "REJECT" }
		t.Logf("  %-8s %-9s %-26s %s", verdict, d.Announcement.Device, d.Reason, d.Detail)
	}

	if len(decisions) != 5 { t.Errorf("decisions=%d; an already-enrolled device must be silent", len(decisions)) }
	byDev := map[string]EnrollmentDecision{}
	for _, d := range decisions { byDev[d.Announcement.Device] = d }

	if !byDev["field-01"].Approve && byDev["field-01"].Reject {
		t.Error("field-01 is legitimate and must not be rejected")
	}
	if byDev["field-01"].Approve { t.Error("must be PENDING without autoApprove — an announcement is a request") }
	if byDev["field-99"].Reason != fleetv1.EnrollRejectNotInTemplate { t.Error("field-99 not caught") }
	if byDev["rogue-01"].Reason != fleetv1.EnrollRejectUnknownModel { t.Error("rogue-01 not caught") }
	if byDev["field-22"].Reason != fleetv1.EnrollRejectDecommissioned { t.Error("decommissioned name reused") }
}

func TestEnrollmentConflicts(t *testing.T) {
	a := ann("field-07", "heltec-v3", "", 0, "v39")
	a.TopicPrefix = "field-07"
	a.BuildTime = "2026-08-30T15:47:48Z"

	c := FlagConflicts(&a, "v42", mustTime("2026-08-30T16:00:00Z"))
	for _, x := range c { t.Logf("  conflict: %s", x) }
	if len(c) != 2 { t.Errorf("conflicts=%d want 2 (stale hash, stale build)", len(c)) }
}

func TestExpirePending(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-30 * 24 * time.Hour))
	fresh := metav1.Now()
	kept, dropped := ExpirePending([]fleetv1.DeviceAnnouncement{
		{Device: "bench-test", LastAnnouncedAt: &old},
		{Device: "field-01", LastAnnouncedAt: &fresh},
	}, 7*24*time.Hour, time.Now())
	t.Logf("  kept=%d dropped=%d", len(kept), dropped)
	if len(kept) != 1 || dropped != 1 { t.Error("stale announcement must expire") }
}

func mustTime(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
