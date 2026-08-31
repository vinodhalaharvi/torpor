// Package v1alpha1 contains the first API this project owns rather than
// inherits.
//
// Everything up to here has been KubeEdge's Device and DeviceStatus with a
// mapper underneath. This is the layer above: state that KubeEdge has no
// vocabulary for, because it assumes a device is either reachable or it is not.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LivenessState is what a device is doing, as opposed to whether anyone can
// reach it.
//
// The distinction is the entire project. Kubernetes has NotReady, which means
// "this cannot be scheduled on" and leads to eviction. A battery node that
// checks in every four days is NotReady by that measure for three days and
// twenty-three hours, and it is perfectly healthy the whole time.
type LivenessState string

const (
	// StateOnline: heard from within the expected interval, and recently
	// enough that a command sent now would plausibly arrive.
	StateOnline LivenessState = "Online"

	// StateSleeping: silent, and expected to be. This is a healthy steady
	// state, not a degraded one. Nothing should be alerted, nothing should be
	// evicted, and a rollout targeting this device is not failing — it is
	// waiting, which is a different thing.
	StateSleeping LivenessState = "Sleeping"

	// StateUnreachable: silent for longer than this device's own tolerance.
	// Still not "failed" — a node under snow, or one whose gateway is down,
	// is Unreachable and entirely undamaged. It means "we have lost sight of
	// it", which is a statement about us.
	StateUnreachable LivenessState = "Unreachable"

	// StateUnknown: never heard from. Distinct from Unreachable, because a
	// device that has never reported may simply not be deployed yet.
	StateUnknown LivenessState = "Unknown"
)

// DeviceLivenessSpec is mostly empty on purpose.
//
// Liveness is derived, not declared. The one thing worth declaring is what
// "normal silence" means for this device, and even that has a sensible source:
// it comes from the Device's protocol config when set there.
type DeviceLivenessSpec struct {
	// DeviceRef names the Device this tracks. Defaults to the object's own
	// name, since one liveness object per device is the only sensible mapping.
	// +optional
	DeviceRef string `json:"deviceRef,omitempty"`

	// ExpectedInterval is how often this device is expected to report.
	// Silence shorter than this is Online; longer is Sleeping.
	//
	// Per-device because a mains gateway and a battery node checking in daily
	// are both healthy at wildly different rates. A single cluster-wide
	// timeout — which is what a Node lease is — cannot express that.
	// +optional
	ExpectedInterval metav1.Duration `json:"expectedInterval,omitempty"`

	// StaleMultiplier: silence beyond ExpectedInterval x this is Unreachable.
	// A judgement call rather than a fact, which is why it is configurable and
	// why three is only a default.
	// +optional
	StaleMultiplier int32 `json:"staleMultiplier,omitempty"`

	// Property is which reported property to measure freshness from. Defaults
	// to the first with a timestamp.
	// +optional
	Property string `json:"property,omitempty"`
}

type DeviceLivenessStatus struct {
	// +optional
	State LivenessState `json:"state,omitempty"`

	// Transport this device is reached over, copied from the Device's labels.
	// Present because state is only interpretable against it: 47 seconds of
	// silence is alarming over WiFi and unremarkable over LoRa.
	// +optional
	Transport string `json:"transport,omitempty"`

	// +optional
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`

	// SilentFor is human-readable and deliberately redundant with LastSeen.
	// It is the column an operator actually reads.
	// +optional
	SilentFor string `json:"silentFor,omitempty"`

	// NextExpectedBy is when silence stops being normal. Empty for a device
	// with no declared interval.
	// +optional
	NextExpectedBy *metav1.Time `json:"nextExpectedBy,omitempty"`

	// Assessment says why the state is what it is, in a form a human can read
	// without knowing the thresholds.
	//
	// This exists because "Sleeping" alone invites the question every alerting
	// system gets wrong: is that bad? WithinExpectedWakeInterval answers it.
	// +optional
	Assessment string `json:"assessment,omitempty"`

	// Gateway this device is reached through, if it has no address of its own.
	// +optional
	Gateway string `json:"gateway,omitempty"`

	// Capability is what this device can currently do, per transport.
	//
	// It lives on the liveness object rather than on KubeEdge's Device because
	// half of it is observed rather than declared — ReachableVia and battery
	// change without anyone editing a spec. A rollout planner reads one object
	// to answer both "is it there" and "can it take this", which are the only
	// two questions it has.
	// +optional
	Capability *DeviceCapability `json:"capability,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=liveness;dl,scope=Namespaced
// +kubebuilder:printcolumn:name="Transport",type=string,JSONPath=`.status.transport`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Silent",type=string,JSONPath=`.status.silentFor`
// +kubebuilder:printcolumn:name="Assessment",type=string,JSONPath=`.status.assessment`
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.status.gateway`,priority=1
type DeviceLiveness struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceLivenessSpec   `json:"spec,omitempty"`
	Status DeviceLivenessStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DeviceLivenessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceLiveness `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceLiveness{}, &DeviceLivenessList{})
}
