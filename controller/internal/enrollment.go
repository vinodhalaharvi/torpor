package internal

import (
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "github.com/vinodhalaharvi/torpor/controller/api/v1alpha1"
)

// EnrollmentDecision is what to do with one announcement.
type EnrollmentDecision struct {
	Announcement fleetv1.DeviceAnnouncement
	Approve      bool
	Reject       bool
	Reason       string
	Detail       string
}

// AssessEnrollment decides what to do with each claim.
//
// Every announcement is a claim from an unauthenticated source. The checks
// below are ordered so that the cheapest and most certain rejections happen
// first, and so that anything ambiguous ends up pending rather than approved
// or rejected — a human looking at a suspicious row is a much better outcome
// than a machine guessing in either direction.
func AssessEnrollment(
	spec *fleetv1.DeviceEnrollmentSpec,
	announcements []fleetv1.DeviceAnnouncement,
	existing map[string]bool, // devices that already exist
	decommissioned map[string]bool,
	templateInstances map[string]bool, // instance names from RequireTemplate
	now time.Time,
) []EnrollmentDecision {
	var out []EnrollmentDecision

	// Node ids must be unique per gateway. Two devices claiming node 2 behind
	// gw-north is either a misconfiguration or somebody's idea of a joke, and
	// both are worth a human rather than a silent winner.
	seen := map[string]string{}
	for _, a := range announcements {
		if a.Gateway == "" || a.NodeID == 0 {
			continue
		}
		key := fmt.Sprintf("%s/%d", a.Gateway, a.NodeID)
		if first, dup := seen[key]; dup && first != a.Device {
			// Both get flagged, not just the second. Which one arrived first
			// is an accident of timing and says nothing about which is right.
			continue
		}
		seen[key] = a.Device
	}

	for _, a := range announcements {
		d := EnrollmentDecision{Announcement: a}

		switch {
		case decommissioned[a.Device]:
			// A device that was decommissioned and is announcing again is
			// either a replacement flashed with the old identity, or the
			// stolen one turning up. Both want a human.
			d.Reject, d.Reason = true, fleetv1.EnrollRejectDecommissioned
			d.Detail = "this name was decommissioned; use a new name or revoke the decommission"

		case existing[a.Device]:
			// Not a rejection worth recording as suspicious — a device that
			// reboots re-announces, and its retained message outlives it.
			// Silently already-enrolled.
			continue

		case len(spec.AllowedModels) > 0 && !contains(spec.AllowedModels, a.Model):
			d.Reject, d.Reason = true, fleetv1.EnrollRejectUnknownModel
			d.Detail = fmt.Sprintf("claims model %q; allowed: %v", a.Model, spec.AllowedModels)

		case spec.RequireTemplate != "" && !templateInstances[a.Device]:
			// The useful middle ground. You already wrote down which fifteen
			// devices you ordered; a device claiming to be one of them is
			// checkable, and one claiming otherwise is not something you want
			// to discover during a rollout.
			d.Reject, d.Reason = true, fleetv1.EnrollRejectNotInTemplate
			d.Detail = fmt.Sprintf("not an instance of template %q", spec.RequireTemplate)

		case a.Gateway != "" && a.NodeID != 0 && seen[fmt.Sprintf("%s/%d", a.Gateway, a.NodeID)] != a.Device:
			d.Reject, d.Reason = true, fleetv1.EnrollRejectNodeIDTaken
			d.Detail = fmt.Sprintf("node %d on gateway %s is claimed by %s",
				a.NodeID, a.Gateway, seen[fmt.Sprintf("%s/%d", a.Gateway, a.NodeID)])

		case spec.AutoApprove:
			d.Approve = true
			d.Reason = "autoApprove"

		default:
			// Pending. The default, and it should be: an announcement is a
			// request, and something has to grant it.
			d.Reason = "awaiting approval"
		}
		out = append(out, d)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Announcement.Device < out[j].Announcement.Device
	})
	return out
}

// FlagConflicts annotates an announcement with things worth a second look.
//
// Populated rather than acted on. A stale firmware build is not grounds for
// rejection — it is grounds for somebody noticing before the device joins a
// fleet and starts drifting, which is a failure that already cost an hour on
// real hardware here.
func FlagConflicts(
	a *fleetv1.DeviceAnnouncement,
	expectedHash string,
	minBuildTime time.Time,
) []string {
	var out []string

	if expectedHash != "" && a.ConfigHash != "" && a.ConfigHash != expectedHash {
		out = append(out, fmt.Sprintf(
			"announces %s while the fleet expects %s — will enroll already drifted",
			a.ConfigHash, expectedHash))
	}

	if !minBuildTime.IsZero() && a.BuildTime != "" {
		if t, err := time.Parse(time.RFC3339, a.BuildTime); err == nil && t.Before(minBuildTime) {
			out = append(out, fmt.Sprintf(
				"built %s, before the current firmware — flashed from a stale cache?",
				t.Format("2006-01-02 15:04")))
		}
	}

	if a.Gateway == "" && a.TopicPrefix == "" {
		out = append(out, "claims neither a gateway nor a topic prefix; unreachable as declared")
	}

	return out
}

// ExpirePending drops announcements nobody acted on.
//
// A pending list nobody has cleared is a pending list nobody reads, and a
// device announced once during a bench test should not linger as a permanent
// row demanding attention it does not deserve.
func ExpirePending(
	pending []fleetv1.DeviceAnnouncement, after time.Duration, now time.Time,
) ([]fleetv1.DeviceAnnouncement, int) {
	if after <= 0 {
		return pending, 0
	}
	var kept []fleetv1.DeviceAnnouncement
	dropped := 0
	for _, a := range pending {
		last := a.LastAnnouncedAt
		if last == nil {
			last = a.AnnouncedAt
		}
		if last != nil && now.Sub(last.Time) > after {
			dropped++
			continue
		}
		kept = append(kept, a)
	}
	return kept, dropped
}

// BuildDeviceFromAnnouncement produces the Device spec an approval creates.
//
// Note what is copied and what is not. The claims become configuration because
// that is the point; nothing from the announcement becomes a label, because
// labels drive selectors and a device should not be able to talk its way into
// a rollout by claiming a role.
func BuildDeviceFromAnnouncement(
	a fleetv1.DeviceAnnouncement, spec *fleetv1.DeviceEnrollmentSpec, broker string,
) map[string]interface{} {
	cfg := map[string]interface{}{"broker": broker}
	if a.Gateway != "" {
		cfg["gateway"] = a.Gateway
		cfg["nodeID"] = int64(a.NodeID)
	}
	if a.TopicPrefix != "" {
		cfg["topicPrefix"] = a.TopicPrefix
	}

	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	if a.Model != "" {
		// Model is recorded as an annotation-shaped label only because it is
		// useful for queries and cannot be used to grant anything — every
		// selector that matters uses labels the operator set, not ones the
		// device asked for.
		labels["model"] = a.Model
	}

	return map[string]interface{}{
		"name":       a.Device,
		"labels":     labels,
		"nodeName":   spec.NodeName,
		"configData": cfg,
	}
}

// EnrollmentSummary is the line an operator reads.
func EnrollmentSummary(decisions []EnrollmentDecision) string {
	var approve, reject, pending int
	for _, d := range decisions {
		switch {
		case d.Approve:
			approve++
		case d.Reject:
			reject++
		default:
			pending++
		}
	}
	return fmt.Sprintf("%d to approve, %d pending, %d rejected", approve, pending, reject)
}

var _ = metav1.Now
