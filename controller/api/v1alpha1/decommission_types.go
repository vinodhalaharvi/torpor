package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceDecommission is how a device stops being part of the fleet without
// pretending it never was.
//
// The obvious implementation is `kubectl delete device`, and it is wrong in
// three ways at once.
//
// It loses the history. A device that ran for four years and was replaced is
// evidence about the model, the site, and the deployment — and deleting the
// object deletes the only record of it.
//
// It corrupts every derived number. A stolen device sits in Unreachable
// forever, and it is counted forever: in drift totals, in credential AtRisk,
// in every fleet query. Six months of accumulated dead devices makes the
// numbers useless, which makes them ignored, which is the actual failure —
// a fleet health metric nobody trusts is worse than none.
//
// And it says nothing about why. "Stolen" and "battery died" and "sold to
// another operator" call for different follow-ups, and the difference is worth
// exactly as much as it costs to record.
//
// So: an explicit terminal state, retained, excluded from live counts, and
// carrying a reason.
type DeviceDecommissionSpec struct {
	// DeviceRef names the device leaving the fleet.
	DeviceRef string `json:"deviceRef"`

	// Reason is required. There is no sensible default, and a decommission
	// without one is a mystery six months later when somebody asks why the
	// site has four sensors instead of five.
	Reason DecommissionReason `json:"reason"`

	// Detail is free text for whoever reads this later. Serial numbers,
	// ticket references, the name of the technician who pulled it.
	// +optional
	Detail string `json:"detail,omitempty"`

	// EffectiveFrom backdates the decommission.
	//
	// Necessary because the paperwork always lags the event. A device stolen
	// on Tuesday and noticed on Friday should not appear as three days of
	// unexplained unreachability in the drift history.
	// +optional
	EffectiveFrom *metav1.Time `json:"effectiveFrom,omitempty"`

	// RevokeCredentials marks the device's credentials as no longer trusted.
	//
	// Default true, and it should be. A device removed from a fleet for theft
	// that keeps working credentials is a security problem, not a bookkeeping
	// one, and the safe default is the one you would choose while calm.
	// +optional
	RevokeCredentials *bool `json:"revokeCredentials,omitempty"`

	// DeleteDeviceObject removes the underlying Device.
	//
	// Default FALSE. This is the whole point of the object existing: the
	// device stops counting without ceasing to have existed. Set it true only
	// when the record genuinely is not wanted, and know that the twin history
	// goes with it.
	// +optional
	DeleteDeviceObject bool `json:"deleteDeviceObject,omitempty"`

	// Replacement names the device that took over, if any.
	//
	// Worth recording because "this site has had four sensors in three years"
	// is a fact about the site, and a chain of replacements is the only way to
	// see it. Without this each replacement looks like a fresh deployment.
	// +optional
	Replacement string `json:"replacement,omitempty"`
}

type DecommissionReason string

const (
	// The device is gone and somebody has it.
	ReasonStolen DecommissionReason = "Stolen"
	// Hardware failure, water ingress, lightning, a cow.
	ReasonFailed DecommissionReason = "Failed"
	// Battery exhausted and not worth replacing.
	ReasonBatteryExpired DecommissionReason = "BatteryExpired"
	// Deliberately removed — site closed, contract ended.
	ReasonRetired DecommissionReason = "Retired"
	// Swapped for a newer unit; Replacement should name it.
	ReasonReplaced DecommissionReason = "Replaced"
	// Ownership transferred. Distinct from Retired because the device is still
	// out there working for somebody else, and its credentials matter.
	ReasonTransferred DecommissionReason = "Transferred"
	// Never actually deployed. A row in a template that turned out wrong.
	ReasonNeverDeployed DecommissionReason = "NeverDeployed"
)

type DeviceDecommissionStatus struct {
	// +optional
	Phase DecommissionPhase `json:"phase,omitempty"`

	// +optional
	DecommissionedAt *metav1.Time `json:"decommissionedAt,omitempty"`

	// LastSeenAlive is copied off the liveness object before it is excluded.
	//
	// The single most useful field here. Once a device stops being counted,
	// this is the only remaining record of how long it actually worked, and
	// service life is the question anybody procuring the next batch will ask.
	// +optional
	LastSeenAlive *metav1.Time `json:"lastSeenAlive,omitempty"`

	// ServiceLife between first contact and last, human-readable.
	// +optional
	ServiceLife string `json:"serviceLife,omitempty"`

	// FinalConfigHash is what it was running when it stopped.
	//
	// Occasionally the most important field in an incident: three devices that
	// died within a week of each other, all on the same firmware, is a pattern
	// that is invisible once the objects are deleted.
	// +optional
	FinalConfigHash string `json:"finalConfigHash,omitempty"`

	// +optional
	CredentialsRevoked bool `json:"credentialsRevoked,omitempty"`
	// +optional
	ExcludedFromQueries bool `json:"excludedFromQueries,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type DecommissionPhase string

const (
	// DecomPending: recorded, not yet applied. Brief.
	DecomPending DecommissionPhase = "Pending"
	// DecomComplete: excluded from live counts, history retained.
	DecomComplete DecommissionPhase = "Complete"
	// DecomBlocked: something refers to this device — an active rollout, a
	// template that would recreate it. Reported rather than forced, because
	// silently decommissioning a device a template will recreate in thirty
	// seconds produces a loop nobody can see.
	DecomBlocked DecommissionPhase = "Blocked"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=decom,scope=Namespaced
// +kubebuilder:printcolumn:name="Device",type=string,JSONPath=`.spec.deviceRef`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.spec.reason`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Service-Life",type=string,JSONPath=`.status.serviceLife`
// +kubebuilder:printcolumn:name="Replacement",type=string,JSONPath=`.spec.replacement`,priority=1
type DeviceDecommission struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceDecommissionSpec   `json:"spec,omitempty"`
	Status DeviceDecommissionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DeviceDecommissionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceDecommission `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceDecommission{}, &DeviceDecommissionList{})
}
