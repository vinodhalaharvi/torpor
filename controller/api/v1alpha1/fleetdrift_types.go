package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------------------
// FleetDrift
// ---------------------------------------------------------------------------

// FleetDrift answers the question people actually check on a Monday morning:
// is device 47 still running what it is supposed to be, six months later?
//
// A rollout tells you whether a change succeeded once. Drift tells you whether
// it stayed true — and things come untrue on their own. A technician reflashes
// an old image. A device is factory reset. Someone changes a setting locally
// to fix something at 3am and never puts it back.
//
// Mechanically this is the health gate's comparison run continuously instead of
// during a rollout. That is why it costs almost nothing to add and why it was
// worth adding: the expensive part — knowing what a device is actually running
// as opposed to what it was told — already exists.
type FleetDriftSpec struct {
	// Selector picks the fleet to watch. Empty means everything.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Expect is what these devices should be running. Usually set by whichever
	// FirmwareRollout last completed, so drift is measured against the last
	// deliberate decision rather than against a number someone typed.
	// +optional
	Expect *DriftExpectation `json:"expect,omitempty"`

	// GracePeriod is how long a device may disagree before it counts as
	// drifted.
	//
	// Necessary because a device mid-rollout disagrees legitimately, and
	// because a sleeping node reports its state late rather than wrongly.
	// Without this every rollout would trip its own drift alarm.
	// +optional
	GracePeriod *metav1.Duration `json:"gracePeriod,omitempty"`

	// IgnoreSleeping excludes devices that are quiet-and-expected-to-be.
	//
	// Default false, deliberately. A sleeping device whose last known state
	// was wrong is still wrong, and hiding it until it wakes is how a fleet
	// quietly diverges for a month. The option exists because during a
	// migration the noise is genuinely unhelpful.
	// +optional
	IgnoreSleeping bool `json:"ignoreSleeping,omitempty"`
}

type DriftExpectation struct {
	// ConfigHash every device should report running.
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// Properties that should hold a given value fleet-wide. Site-specific
	// settings do not belong here; this is for things that should be uniform.
	// +optional
	Properties map[string]string `json:"properties,omitempty"`
}

type FleetDriftStatus struct {
	// +optional
	Total int32 `json:"total,omitempty"`
	// +optional
	Converged int32 `json:"converged,omitempty"`
	// +optional
	Drifted int32 `json:"drifted,omitempty"`

	// Unknown: devices whose state cannot be established. Counted separately
	// from drifted, because "we do not know" and "it is wrong" warrant
	// different responses and merging them makes the number useless.
	// +optional
	Unknown int32 `json:"unknown,omitempty"`

	// OldestDrift is the single most useful number here. A fleet with 5%
	// drifted for an hour is mid-rollout. A fleet with 5% drifted for three
	// months has a process problem, and the percentage looks identical.
	// +optional
	OldestDrift string `json:"oldestDrift,omitempty"`

	// +optional
	Devices []DriftedDevice `json:"devices,omitempty"`
	// +optional
	LastChecked *metav1.Time `json:"lastChecked,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type DriftedDevice struct {
	Device string `json:"device"`
	// +optional
	Property string `json:"property,omitempty"`
	// +optional
	Expected string `json:"expected,omitempty"`
	// +optional
	Actual string `json:"actual,omitempty"`
	// +optional
	DriftAge string `json:"driftAge,omitempty"`

	// Assessment says whether this drift is a problem yet.
	//
	// The row this exists for: a battery node silent for three days, whose
	// last report disagreed, and which checks in every four days. Drifted,
	// unreachable, and entirely fine. Every monitoring system available would
	// have paged somebody.
	// +optional
	Assessment string `json:"assessment,omitempty"`

	// Remediable records whether this device can even receive the correction.
	// A LoRa-only node drifted on a property that only fits over WiFi is
	// drifted and unfixable until it comes into range, which is a materially
	// different situation from drifted and one write away.
	// +optional
	Remediable bool `json:"remediable,omitempty"`
}

const (
	DriftWithinGrace     = "WithinGracePeriod"
	DriftDeviceSleeping  = "DriftedButSleeping"
	DriftNotRemediable   = "DriftedNoCapableTransport"
	// Capable, online, and the door that could fix it is shut. The drift
	// equivalent of a rollout's AwaitingTransportWindow.
	DriftAwaitingWindow  = "DriftedAwaitingTransportWindow"
	DriftNeedsAttention  = "DriftedAndReachable"
	DriftStateUnknown    = "StateUnknown"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=drift,scope=Namespaced
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.total`
// +kubebuilder:printcolumn:name="Converged",type=integer,JSONPath=`.status.converged`
// +kubebuilder:printcolumn:name="Drifted",type=integer,JSONPath=`.status.drifted`
// +kubebuilder:printcolumn:name="Unknown",type=integer,JSONPath=`.status.unknown`
// +kubebuilder:printcolumn:name="Oldest",type=string,JSONPath=`.status.oldestDrift`
type FleetDrift struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FleetDriftSpec   `json:"spec,omitempty"`
	Status FleetDriftStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FleetDriftList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FleetDrift `json:"items"`
}

// ---------------------------------------------------------------------------
// CredentialExpiry
// ---------------------------------------------------------------------------

// CredentialExpiry is a rollout with a deadline, and the deadline is the whole
// difference.
//
// Every device holds something that expires — a certificate, a token, a
// pre-shared key. Rotating one is a property write. Rotating a fleet is a
// rollout. But a firmware rollout that fails leaves a device running old
// firmware, which is a nuisance; a credential rotation that fails leaves a
// device *permanently unreachable*, because the thing you would use to fix it
// is the thing that expired.
//
// There is no fallback. That is why this is a separate object rather than a
// FirmwareRollout with a date field: the failure is categorically worse and
// deserves its own alarm.
//
// And it is where the capability model earns its keep. A node reachable only
// over LoRa cannot receive a 2 KB certificate — not slowly, not ever. Today
// that is an interesting fact. Ninety days before expiry it is a work order,
// and knowing it ninety days out rather than on the morning it goes dark is a
// question only a capability model can answer.
type CredentialExpirySpec struct {
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Property the device reports its credential expiry through, as RFC3339 or
	// unix seconds. The device knows when its own certificate dies; nothing
	// else reliably does.
	// +optional
	ExpiryProperty string `json:"expiryProperty,omitempty"`

	// WarnBefore is when to start reporting. Long by default, because the
	// useful output of this object is a work order rather than an alert —
	// a truck roll to a remote site is scheduled in weeks, not hours.
	// +optional
	WarnBefore *metav1.Duration `json:"warnBefore,omitempty"`

	// RotationSizeBytes is how large the new credential is, checked against
	// each transport's capacity. This is what turns "expires in 60 days" into
	// "expires in 60 days and cannot be rotated remotely."
	// +optional
	RotationSizeBytes int64 `json:"rotationSizeBytes,omitempty"`

	// RequiredContactWindow is how long a device must be reachable for to
	// complete a rotation. A node that surfaces for thirty seconds a day
	// cannot receive a certificate that takes two minutes to transfer, and
	// being briefly reachable is not the same as being rotatable.
	// +optional
	RequiredContactWindow *metav1.Duration `json:"requiredContactWindow,omitempty"`
}

type CredentialExpiryStatus struct {
	// +optional
	Total int32 `json:"total,omitempty"`
	// +optional
	Healthy int32 `json:"healthy,omitempty"`
	// +optional
	Expiring int32 `json:"expiring,omitempty"`
	// +optional
	Expired int32 `json:"expired,omitempty"`

	// AtRisk is the number this object exists for: devices that will expire
	// and cannot be rotated over any transport they have.
	//
	// Every one of these is a scheduled truck roll or a device that goes dark.
	// No other tool can compute it, because computing it requires knowing what
	// a device can receive rather than merely whether it is online.
	// +optional
	AtRisk int32 `json:"atRisk,omitempty"`

	// NextExpiry is the fleet's deadline. The date somebody has to act by.
	// +optional
	NextExpiry *metav1.Time `json:"nextExpiry,omitempty"`

	// +optional
	Devices []CredentialStatus `json:"devices,omitempty"`
	// +optional
	LastChecked *metav1.Time `json:"lastChecked,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type CredentialStatus struct {
	Device string `json:"device"`
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// +optional
	TimeLeft string `json:"timeLeft,omitempty"`
	// +optional
	State string `json:"state,omitempty"`

	// Rotatable: is there any transport on this device that could carry the
	// new credential, ever.
	// +optional
	Rotatable bool `json:"rotatable,omitempty"`

	// RotatableVia names the transports that could. Empty with an approaching
	// expiry is the row that means somebody is driving out there.
	// +optional
	RotatableVia []string `json:"rotatableVia,omitempty"`

	// +optional
	Reason string `json:"reason,omitempty"`

	// ActionRequired is written for a human, in the form of the decision they
	// have to make. "Schedule site visit before 2026-11-14" rather than
	// "rotation_capability=false".
	// +optional
	ActionRequired string `json:"actionRequired,omitempty"`
}

const (
	CredHealthy = "Healthy"
	// CredExpiring: inside the warning window and rotatable. Ordinary work.
	CredExpiring = "Expiring"
	// CredAtRisk: will expire, cannot be rotated over anything it has. The
	// device goes dark on a known date unless somebody visits it.
	CredAtRisk = "AtRisk"
	// CredExpired: already dark. Recorded rather than alerted, because the
	// moment to act was months ago and this object existed to prevent it.
	CredExpired = "Expired"
	// CredUnknown: the device has never reported an expiry. Arguably worse
	// than AtRisk, since at least AtRisk has a date.
	CredUnknown = "Unknown"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=creds;expiry,scope=Namespaced
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.total`
// +kubebuilder:printcolumn:name="Expiring",type=integer,JSONPath=`.status.expiring`
// +kubebuilder:printcolumn:name="At-Risk",type=integer,JSONPath=`.status.atRisk`
// +kubebuilder:printcolumn:name="Expired",type=integer,JSONPath=`.status.expired`
// +kubebuilder:printcolumn:name="Next-Expiry",type=string,JSONPath=`.status.nextExpiry`
type CredentialExpiry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CredentialExpirySpec   `json:"spec,omitempty"`
	Status CredentialExpiryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CredentialExpiryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CredentialExpiry `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&FleetDrift{}, &FleetDriftList{},
		&CredentialExpiry{}, &CredentialExpiryList{},
	)
}
