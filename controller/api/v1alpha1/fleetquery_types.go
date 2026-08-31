package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FleetQuery is a selector over things labels cannot express.
//
// Kubernetes selects by label because a pod's interesting properties are
// things somebody decided: which app, which tier, which version. A device's
// interesting properties are things the world decided — what it is running,
// whether it can be reached, how much battery it has left, whether it can
// accept a firmware image at all.
//
// You cannot label your way to "everything that has drifted and can still be
// fixed remotely", because both halves change without anybody editing a label,
// and a label that goes stale is worse than no label.
//
// So this selects on derived state. Which makes it the object a rollout
// selector arguably *should* accept: "roll out to everything drifted and
// remediable" is a better instruction than a label match, and it stays true as
// the fleet changes underneath it.
type FleetQuerySpec struct {
	// Labels narrows the candidate set first, cheaply. Optional, and mostly
	// useful for scoping to a site or a role before asking harder questions.
	// +optional
	Labels *metav1.LabelSelector `json:"labels,omitempty"`

	// Where is the interesting part: predicates over derived state.
	// +optional
	Where *QueryPredicate `json:"where,omitempty"`

	// Limit caps the device list in status. The counts stay accurate.
	//
	// Necessary because a query matching 5000 devices would otherwise write an
	// object too large for etcd, and a fleet query that breaks on large fleets
	// is a fleet query that only works on toys.
	// +optional
	Limit int32 `json:"limit,omitempty"`
}

// QueryPredicate is a conjunction. Every field set must hold.
//
// Deliberately not a general expression language. An operator reading this at
// 3am should be able to tell what it selects without evaluating precedence
// rules, and every query anybody has actually wanted so far is an AND of a few
// conditions.
type QueryPredicate struct {
	// State matches liveness: Online, Sleeping, Unreachable, Unknown.
	// +optional
	State *StringMatch `json:"state,omitempty"`

	// Transport matches any declared transport type.
	// +optional
	Transport *StringMatch `json:"transport,omitempty"`

	// ReachableVia matches transports that are up *right now*, which is a
	// different question from Transport and the difference is the point:
	// a node with WiFi declared is not a node with WiFi available.
	// +optional
	ReachableVia *StringMatch `json:"reachableVia,omitempty"`

	// RunningConfigHash matches what the device reports it is running.
	// notIn is the useful direction — "everything that is not on v42" is the
	// drift question, and it stays correct as v42 rolls out.
	// +optional
	RunningConfigHash *StringMatch `json:"runningConfigHash,omitempty"`

	// OTACapable: does any transport on this device carry firmware, ever.
	// Permanent capability rather than current reachability.
	// +optional
	OTACapable *bool `json:"otaCapable,omitempty"`

	// OTAReachable: is a firmware-capable transport up right now. The
	// intersection that decides whether an update can start this minute.
	// +optional
	OTAReachable *bool `json:"otaReachable,omitempty"`

	// SilentLongerThan finds devices that have gone quiet, measured against
	// the wall clock rather than their own expectations.
	// +optional
	SilentLongerThan *metav1.Duration `json:"silentLongerThan,omitempty"`

	// SilentBeyondExpected finds devices quiet for longer than *their own*
	// declared interval.
	//
	// The distinction matters more than it looks. A daily-checkin node silent
	// for six hours is fine; an every-30s node silent for six hours is gone.
	// A wall-clock threshold cannot tell those apart, and a fleet-wide alert
	// built on one will be wrong in both directions simultaneously.
	// +optional
	SilentBeyondExpected *bool `json:"silentBeyondExpected,omitempty"`

	// BatteryBelowPercent, for planning site visits.
	// +optional
	BatteryBelowPercent *int32 `json:"batteryBelowPercent,omitempty"`

	// Not inverts the whole predicate. One level only — nesting invites
	// queries nobody can read.
	// +optional
	Not *QueryPredicate `json:"not,omitempty"`
}

// StringMatch is in / notIn / equals. No regex, on purpose: a regex in a
// selector is a way to accidentally match a device you have never heard of.
type StringMatch struct {
	// +optional
	Equals string `json:"equals,omitempty"`
	// +optional
	In []string `json:"in,omitempty"`
	// +optional
	NotIn []string `json:"notIn,omitempty"`
}

type FleetQueryStatus struct {
	// +optional
	Matched int32 `json:"matched,omitempty"`
	// +optional
	Evaluated int32 `json:"evaluated,omitempty"`

	// Devices is the matched list, truncated to Limit.
	// +optional
	Devices []QueryResult `json:"devices,omitempty"`

	// Truncated says the list is short while the counts are not, so nobody
	// builds a rollout from a partial answer believing it is complete.
	// +optional
	Truncated bool `json:"truncated,omitempty"`

	// Summary groups the matches by why they matched.
	//
	// The number is rarely the useful output. "23 devices are drifted" prompts
	// the question "and how many of those can I actually fix", which is the
	// answer somebody needs before deciding whether this is a rollout or a
	// truck schedule.
	// +optional
	Summary map[string]int32 `json:"summary,omitempty"`

	// +optional
	LastEvaluated *metav1.Time `json:"lastEvaluated,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type QueryResult struct {
	Device string `json:"device"`
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	Transport string `json:"transport,omitempty"`
	// +optional
	RunningConfigHash string `json:"runningConfigHash,omitempty"`
	// +optional
	SilentFor string `json:"silentFor,omitempty"`

	// Actionable is the column that turns a list into a plan: can this device
	// be reached and changed right now, or does it need somebody to drive out.
	// +optional
	Actionable bool `json:"actionable,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fq;query,scope=Namespaced
// +kubebuilder:printcolumn:name="Matched",type=integer,JSONPath=`.status.matched`
// +kubebuilder:printcolumn:name="Evaluated",type=integer,JSONPath=`.status.evaluated`
// +kubebuilder:printcolumn:name="Truncated",type=boolean,JSONPath=`.status.truncated`,priority=1
// +kubebuilder:printcolumn:name="Last-Run",type=string,JSONPath=`.status.lastEvaluated`
type FleetQuery struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FleetQuerySpec   `json:"spec,omitempty"`
	Status FleetQueryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FleetQueryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FleetQuery `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FleetQuery{}, &FleetQueryList{})
}
