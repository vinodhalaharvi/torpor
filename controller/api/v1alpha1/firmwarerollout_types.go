package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FirmwareRollout is a Deployment for things that are on your desk.
//
// The ergonomics are stolen from Deployment and Argo Rollouts deliberately —
// selector, strategy, steps, status, `kubectl rollout undo` — because anyone
// who knows Kubernetes should be able to guess the commands. The semantics
// diverge in four places, and each divergence is the reason this exists:
//
//  1. There is no ReplicaSet. A ReplicaSet exists because pods are fungible:
//     you do not update a pod, you kill it and make another. A device is a
//     physical object and every rollout is in-place.
//
//  2. maxUnavailable assumes you control availability. You do not. A device is
//     asleep whether or not the rollout wants it awake, so concurrency here is
//     a prediction rather than a limit.
//
//  3. A Deployment cannot be refused. It schedules or it stays Pending, and
//     Pending means "no capacity yet" — temporary by assumption. Refused is
//     permanent and correct, and no Kubernetes verb means it.
//
//  4. Readiness is a probe the kubelet initiates. A device reports when it
//     feels like it, and silence means nothing without knowing its expected
//     interval. That is why DeviceLiveness had to come first.
type FirmwareRolloutSpec struct {
	// Selector picks the target fleet. The first thing in this project that
	// addresses many devices as one.
	Selector *metav1.LabelSelector `json:"selector"`

	Source FirmwareSource `json:"source"`

	// Requires is checked BEFORE anything is transmitted, and produces the
	// Refused list rather than a batch of timeouts six hours later. This is
	// the field the whole project argues for.
	// +optional
	Requires *RolloutRequirements `json:"requires,omitempty"`

	// +optional
	Strategy RolloutStrategy `json:"strategy,omitempty"`

	// Paused halts progression without abandoning the rollout. Devices already
	// updated stay updated.
	// +optional
	Paused bool `json:"paused,omitempty"`
}

type FirmwareSource struct {
	// Package is where the artifact lives. OCI because a firmware image is a
	// content-addressed blob and registries already solve distribution.
	// +optional
	Package string `json:"package,omitempty"`

	// ConfigHash is what the device reports back to prove it applied this.
	//
	// It has to be something the device computes about its own running state,
	// not an echo of what it was told. A device can report the value it was
	// handed without running it — that failure has already happened once in
	// this project, with a tone that never played while every signal said
	// converged. Health gates must observe consequences, not acknowledgements.
	ConfigHash string `json:"configHash"`

	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
}

// RolloutRequirements is what a device must be able to do to be eligible.
//
// Nothing in Kubernetes has this shape, because a pod's target can always
// eventually run it. A device may be permanently, structurally incapable —
// a LoRa-only node will never accept a megabyte, no matter how long you wait.
type RolloutRequirements struct {
	// OTA: the device needs a transport that can carry firmware at all.
	// +optional
	OTA bool `json:"ota,omitempty"`

	// MinBatteryPercent refuses devices that would be left too low.
	//
	// A rollout refused on ENERGY grounds is something no platform models.
	// Memfault knows a device has a battery; it does not know that updating it
	// costs a measurable fraction of what remains.
	// +optional
	MinBatteryPercent int32 `json:"minBatteryPercent,omitempty"`

	// MaxSilentFor excludes devices already out of contact. Rolling firmware
	// to a fleet you have lost sight of is how you lose it permanently.
	// +optional
	MaxSilentFor *metav1.Duration `json:"maxSilentFor,omitempty"`
}

type RolloutStrategy struct {
	// Canary is how many devices go first. One, usually. The point is not
	// throughput, it is having something to look at before committing.
	// +optional
	Canary int32 `json:"canary,omitempty"`

	// Steps are cumulative percentages: [1, 10, 50, 100]. Borrowed wholesale
	// from Argo Rollouts, including the ergonomics of `kubectl get -w`.
	// +optional
	Steps []int32 `json:"steps,omitempty"`

	// +optional
	HealthGate *HealthGate `json:"healthGate,omitempty"`

	// +optional
	RollbackOn *RollbackTriggers `json:"rollbackOn,omitempty"`

	// MaxConcurrent bounds simultaneous transfers.
	//
	// Not a performance knob. On a shared medium — LoRa duty cycle, an RS-485
	// segment, a Thread mesh partition — the channel is the scarce resource,
	// and exceeding it is a regulatory problem rather than a slow one.
	// +optional
	MaxConcurrent int32 `json:"maxConcurrent,omitempty"`
}

// HealthGate is what "healthy" means for this rollout.
//
// Shaped after Argo Rollouts' AnalysisTemplate: a named, declarative check
// rather than logic inlined in a controller, so battery-drain and boot-loop
// checks can be added without changing the rollout spec.
type HealthGate struct {
	// MustReportWithin: the device has to be heard from within this window
	// after the update. Silence is the only universally available signal from
	// a device you cannot poll.
	// +optional
	MustReportWithin *metav1.Duration `json:"mustReportWithin,omitempty"`

	// MustReportConfigHash requires the device to report the hash it is
	// actually running. Stronger than MustReportWithin, which only proves it
	// is alive — not that the update took.
	// +optional
	MustReportConfigHash bool `json:"mustReportConfigHash,omitempty"`

	// SettleFor is how long health must hold before the step is accepted.
	// A device that boots, reports, and then boot-loops is not healthy, and
	// checking once immediately after the update would call it so.
	// +optional
	SettleFor *metav1.Duration `json:"settleFor,omitempty"`
}

type RollbackTriggers struct {
	// +optional
	BootLoopThreshold int32 `json:"bootLoopThreshold,omitempty"`

	// HealthGateFailures is how many devices may fail before the whole rollout
	// stops. Default 1 — on hardware you cannot recreate, the right instinct
	// is to stop early and think.
	// +optional
	HealthGateFailures int32 `json:"healthGateFailures,omitempty"`

	// +optional
	Automatic bool `json:"automatic,omitempty"`
}

type RolloutPhase string

const (
	PhasePending     RolloutPhase = "Pending"
	PhasePlanning    RolloutPhase = "Planning"
	PhaseCanary      RolloutPhase = "Canary"
	PhaseProgressing RolloutPhase = "Progressing"

	// PhasePaused: stopped and awaiting a human. Reached by a failed health
	// gate or spec.paused. Devices already updated stay updated.
	PhasePaused RolloutPhase = "Paused"

	// PhaseWaiting: nothing is wrong and nothing can proceed. Every remaining
	// device is asleep or out of range. A legitimate long-lived state — a
	// rollout may sit here for days and be entirely on track.
	PhaseWaiting RolloutPhase = "Waiting"

	// PhaseBlocked: we are not permitted to act, by policy rather than by
	// circumstance.
	//
	// Distinct from Waiting on purpose. Waiting means the devices are not
	// available; Blocked means they are available and we have been told not to
	// touch them. Reporting a change freeze as "waiting for devices" sends
	// somebody to debug a radio for an afternoon.
	PhaseBlocked RolloutPhase = "Blocked"

	PhaseComplete   RolloutPhase = "Complete"
	PhaseRolledBack RolloutPhase = "RolledBack"
)

type FirmwareRolloutStatus struct {
	// +optional
	Phase RolloutPhase `json:"phase,omitempty"`
	// +optional
	Step string `json:"step,omitempty"`

	// Target is everything the selector matched. Eligible is what survived the
	// capability check. The difference between them is the number that does
	// not exist in any other tool.
	// +optional
	Target int32 `json:"target,omitempty"`
	// +optional
	Eligible int32 `json:"eligible,omitempty"`
	// +optional
	Updated int32 `json:"updated,omitempty"`
	// +optional
	Healthy int32 `json:"healthy,omitempty"`

	// Refused: structurally incapable. Not a failure — a planning result,
	// computed before the first byte moves.
	// +optional
	Refused []DeviceOutcome `json:"refused,omitempty"`

	// Pending: capable, not currently reachable. Also not a failure. A node
	// that is WiFi-capable only when a truck drives past is pending until the
	// truck drives past.
	// +optional
	Pending []DeviceOutcome `json:"pending,omitempty"`

	// Failed: attempted and did not take. The only one of the three that
	// warrants waking anybody up.
	// +optional
	Failed []DeviceOutcome `json:"failed,omitempty"`

	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	PreviousConfigHash string `json:"previousConfigHash,omitempty"`

	// BlockedBy names the MaintenanceWindow currently withholding permission,
	// and NextWindow is when it opens.
	//
	// Both exist so that "why is nothing happening" is answerable from the
	// rollout object rather than by going and reading somebody else's policy.
	// +optional
	BlockedBy string `json:"blockedBy,omitempty"`
	// +optional
	BlockReason string `json:"blockReason,omitempty"`
	// +optional
	NextWindow *metav1.Time `json:"nextWindow,omitempty"`

	// DispatchedThisWindow counts against MaxDevicesPerWindow. Reset when a
	// window reopens, because the limit is a blast radius per opportunity to
	// notice, not a total.
	// +optional
	DispatchedThisWindow int32 `json:"dispatchedThisWindow,omitempty"`
	// +optional
	WindowOpenedAt *metav1.Time `json:"windowOpenedAt,omitempty"`
}

// DeviceOutcome records what happened to one device and, more usefully, why.
//
// Reason is machine-readable and Detail is for a human at 3am who has never
// read this code. "NoOTACapableTransport" plus "only transport is lora
// (ota: false)" answers both audiences without either consulting the other.
type DeviceOutcome struct {
	Device string `json:"device"`
	Reason string `json:"reason"`
	// +optional
	Detail string `json:"detail,omitempty"`
	// +optional
	Since *metav1.Time `json:"since,omitempty"`
}

const (
	ReasonNoOTATransport   = "NoOTACapableTransport"
	ReasonBatteryTooLow    = "OTAExceedsBatteryBudget"
	ReasonArtifactTooLarge = "ArtifactExceedsTransportCapacity"
	ReasonAwaitingWindow   = "AwaitingTransportWindow"
	ReasonAsleep           = "DeviceSleeping"
	ReasonUnreachable      = "DeviceUnreachable"
	ReasonHealthGateFail   = "HealthGateFailed"
	ReasonBootLoop         = "BootLoopDetected"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fr;rollout,scope=Namespaced
// +kubebuilder:printcolumn:name="Target",type=integer,JSONPath=`.status.target`
// +kubebuilder:printcolumn:name="Eligible",type=integer,JSONPath=`.status.eligible`
// +kubebuilder:printcolumn:name="Refused",type=integer,JSONPath=`.status.refused`
// +kubebuilder:printcolumn:name="Updated",type=integer,JSONPath=`.status.updated`
// +kubebuilder:printcolumn:name="Healthy",type=integer,JSONPath=`.status.healthy`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Step",type=string,JSONPath=`.status.step`
type FirmwareRollout struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirmwareRolloutSpec   `json:"spec,omitempty"`
	Status FirmwareRolloutStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FirmwareRolloutList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirmwareRollout `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirmwareRollout{}, &FirmwareRolloutList{})
}
