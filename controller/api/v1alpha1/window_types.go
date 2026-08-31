package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Two things get called "scheduling windows" and they are not the same thing.
//
// A MaintenanceWindow is policy. Somebody decided not to touch the pumps during
// the day shift. It constrains us, it is knowable in advance, and it is
// enforced.
//
// A ContactWindow is physics. The node's WiFi is up when the truck drives past,
// and neither we nor the node control when that is. It constrains us, it is
// *predicted* rather than known, and it can only be observed and waited for.
//
// Conflating them produces a scheduler that either treats an observation as a
// promise, or treats a policy as something it can wait out. Both are wrong in
// ways that only show up in production.

// MaintenanceWindow is when we are permitted to act.
//
// Deliberately a separate object rather than a field on FirmwareRollout, for
// the same reason a NetworkPolicy is not a field on a Pod: it is written by a
// different person, on a different schedule, and it applies to everything that
// touches those devices — rollouts, credential rotations, and whatever comes
// later.
type MaintenanceWindowSpec struct {
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Allow lists the periods during which mutation is permitted. Empty means
	// always, which is the right default for a bench and the wrong one for a
	// water utility.
	// +optional
	Allow []WindowPeriod `json:"allow,omitempty"`

	// Deny overrides Allow. Written separately because "never during the
	// harvest" is easier to state as an exception than to encode as a hole in
	// a weekly schedule.
	// +optional
	Deny []WindowPeriod `json:"deny,omitempty"`

	// Timezone the schedule is interpreted in, IANA form. Required in practice
	// even though it looks optional: a maintenance window that silently means
	// UTC will be wrong twice a year in every country that observes daylight
	// saving, and it will be wrong at 2am.
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// AllowInFlightToComplete lets an operation that started inside the window
	// finish outside it.
	//
	// Default true, because the alternative is worse: aborting a firmware
	// transfer at the window boundary leaves a device half-written, and a
	// half-written device is a brick. A window is about when to *start*.
	// +optional
	AllowInFlightToComplete *bool `json:"allowInFlightToComplete,omitempty"`

	// MaxDevicesPerWindow caps how much can be attempted in one window.
	//
	// Not a rate limit. It is a blast-radius limit — if an update turns out to
	// be bad, this is how many devices are broken before anyone notices, and
	// on hardware that is a number somebody wants to choose deliberately.
	// +optional
	MaxDevicesPerWindow int32 `json:"maxDevicesPerWindow,omitempty"`
}

// WindowPeriod is one recurring or absolute period.
type WindowPeriod struct {
	// Cron in standard five-field form, e.g. "0 2 * * *".
	// +optional
	Cron string `json:"cron,omitempty"`

	// Duration the window stays open once Cron fires.
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`

	// Absolute period, for one-off change freezes.
	// +optional
	From *metav1.Time `json:"from,omitempty"`
	// +optional
	Until *metav1.Time `json:"until,omitempty"`

	// Reason is shown to whoever wonders why their rollout is waiting. Worth
	// requiring in practice: "why is nothing happening" is the question this
	// object generates, and the answer should be in the object.
	// +optional
	Reason string `json:"reason,omitempty"`
}

type MaintenanceWindowStatus struct {
	// +optional
	Open bool `json:"open,omitempty"`
	// +optional
	NextOpen *metav1.Time `json:"nextOpen,omitempty"`
	// +optional
	NextClose *metav1.Time `json:"nextClose,omitempty"`
	// +optional
	MatchedDevices int32 `json:"matchedDevices,omitempty"`
	// Reason names the period currently governing, so a paused rollout can say
	// which rule is holding it rather than merely that one is.
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mw;window,scope=Namespaced
// +kubebuilder:printcolumn:name="Open",type=boolean,JSONPath=`.status.open`
// +kubebuilder:printcolumn:name="Devices",type=integer,JSONPath=`.status.matchedDevices`
// +kubebuilder:printcolumn:name="Next-Open",type=string,JSONPath=`.status.nextOpen`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
type MaintenanceWindow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MaintenanceWindowSpec   `json:"spec,omitempty"`
	Status MaintenanceWindowStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MaintenanceWindowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MaintenanceWindow `json:"items"`
}

// ContactWindow is when a device is *expected* to be reachable, learned from
// when it has actually been reachable.
//
// This is not policy and it is not a promise. A node that has surfaced every
// morning for three weeks will probably surface tomorrow, and "probably" is the
// strongest word available. The whole object exists to make that uncertainty
// explicit rather than letting a scheduler assume either extreme.
//
// It lives on DeviceLiveness status because it is observed, not declared —
// nobody types this, it accumulates.
type ContactWindow struct {
	// Transport this describes. A node can have a LoRa contact pattern and a
	// WiFi one that look nothing alike, which is the entire reason the
	// transport ladder exists.
	Transport string `json:"transport"`

	// TypicalInterval between contacts, observed.
	// +optional
	TypicalInterval *metav1.Duration `json:"typicalInterval,omitempty"`

	// TypicalDuration a contact lasts. The number that decides whether a
	// credential rotation can complete: being reachable for thirty seconds is
	// not the same as being rotatable if the transfer takes two minutes.
	// +optional
	TypicalDuration *metav1.Duration `json:"typicalDuration,omitempty"`

	// NextExpected, extrapolated. A prediction, and labelled as one.
	// +optional
	NextExpected *metav1.Time `json:"nextExpected,omitempty"`

	// Confidence in that prediction: High, Medium, Low, None.
	//
	// Present because a scheduler that treats a guess as a fact will schedule
	// a rollout for 04:00 and report failure at 04:05 when the truck is late.
	// A low-confidence window means "wait and watch", not "act then".
	// +optional
	Confidence string `json:"confidence,omitempty"`

	// SamplesObserved is how much evidence sits behind all of the above. Two
	// contacts is a coincidence.
	// +optional
	SamplesObserved int32 `json:"samplesObserved,omitempty"`
}

const (
	ConfidenceHigh   = "High"   // regular, many samples, low variance
	ConfidenceMedium = "Medium" // regular enough to plan around, loosely
	ConfidenceLow    = "Low"    // a pattern may exist; do not schedule on it
	ConfidenceNone   = "None"   // not enough samples to say anything
)

func init() {
	SchemeBuilder.Register(&MaintenanceWindow{}, &MaintenanceWindowList{})
}
