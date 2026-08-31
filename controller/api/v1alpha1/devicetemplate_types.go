package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceTemplate is two hundred devices that are the same except where they
// are not.
//
// Kustomize and Helm solve this for pods, and neither is a good fit here. A
// pod's variation is environmental — image tag, replica count, namespace — and
// it lives in a values file next to the chart. A device's variation is
// physical: this sensor sits in the north pump house and reads three degrees
// high, and that fact belongs with the device rather than with the template.
//
// So instantiation is per-device and the overrides live on the Device, not in a
// values file. The template says what is common; the device says what is not;
// and the answer to "why is this one different" is on the object rather than in
// a directory somebody has to find.
type DeviceTemplateSpec struct {
	// DeviceModelRef is the model every instance uses.
	DeviceModelRef string `json:"deviceModelRef"`

	// Protocol is the shared protocol config. Per-device values — a topic
	// prefix, a gateway, a node id — come from Instances below.
	// +optional
	Protocol *TemplateProtocol `json:"protocol,omitempty"`

	// Properties every instance gets. Collect cycles, visitor topics, the
	// things that are genuinely identical across a fleet.
	// +optional
	Properties []TemplateProperty `json:"properties,omitempty"`

	// Labels applied to every generated Device, merged with per-instance ones.
	//
	// The reason templating and FleetQuery belong together: a template that
	// stamps `role: field-sensor` onto two hundred devices is what makes a
	// selector over those devices mean something.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Instances is the per-device part.
	//
	// Deliberately inline rather than a reference to a ConfigMap or a
	// generator. A fleet's device list is the most valuable thing in the
	// cluster and it should be reviewable in a diff.
	// +optional
	Instances []TemplateInstance `json:"instances,omitempty"`

	// Prune deletes generated Devices whose instance has been removed.
	//
	// Default false, and the default is the important part. A device that
	// disappears from a template is far more likely to be an editing mistake
	// than a decommissioning, and the cost of the two errors is not
	// symmetrical: a stale Device object is clutter, while a deleted one takes
	// its twin history with it. Decommissioning has its own object precisely
	// so it never happens by accident.
	// +optional
	Prune bool `json:"prune,omitempty"`
}

type TemplateProtocol struct {
	Name string `json:"name"`

	// ConfigData shared by every instance. Values may contain {{ .field }}
	// references to instance variables.
	// +optional
	ConfigData map[string]string `json:"configData,omitempty"`
}

type TemplateProperty struct {
	Name string `json:"name"`
	// +optional
	CollectCycle int64 `json:"collectCycle,omitempty"`
	// +optional
	ReportCycle int64 `json:"reportCycle,omitempty"`
	// +optional
	ReportToCloud bool `json:"reportToCloud,omitempty"`
	// +optional
	Visitors map[string]string `json:"visitors,omitempty"`

	// Desired is the fleet-wide value for this property, if any.
	//
	// Overridable per instance, which is the whole point: sample_interval is
	// 300 everywhere except the two sites with a regulator watching, and
	// expressing that as an exception rather than as two templates keeps the
	// exception visible.
	// +optional
	Desired string `json:"desired,omitempty"`
}

// TemplateInstance is one device's worth of difference.
type TemplateInstance struct {
	Name string `json:"name"`

	// Vars substitute into the template's {{ .field }} references. Site names,
	// addresses, gateway assignments.
	// +optional
	Vars map[string]string `json:"vars,omitempty"`

	// Labels merged over the template's.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Overrides replace a property's desired value for this device only.
	//
	// Calibration offsets live here. They are the reason a template cannot
	// simply be a Helm chart: an offset is a measured property of one physical
	// object, discovered after deployment, and it belongs next to that object
	// rather than in a values file thirty entries long.
	// +optional
	Overrides map[string]string `json:"overrides,omitempty"`
}

type DeviceTemplateStatus struct {
	// +optional
	Instances int32 `json:"instances,omitempty"`
	// +optional
	Created int32 `json:"created,omitempty"`
	// +optional
	Updated int32 `json:"updated,omitempty"`

	// Orphaned counts generated Devices whose instance is gone while Prune is
	// off. Surfaced rather than silently ignored, because "I deleted it from
	// the template and nothing happened" should be visible somewhere.
	// +optional
	Orphaned int32 `json:"orphaned,omitempty"`

	// Drifted counts generated Devices edited outside the template.
	//
	// Not an error. Somebody fixing a device at 3am is the system working, and
	// the template should report the divergence rather than reverting it —
	// stomping a manual fix during an incident is how a control plane loses
	// the trust of the people operating it.
	// +optional
	Drifted []string `json:"drifted,omitempty"`

	// +optional
	Errors []TemplateError `json:"errors,omitempty"`
	// +optional
	LastApplied *metav1.Time `json:"lastApplied,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type TemplateError struct {
	Instance string `json:"instance"`
	Reason   string `json:"reason"`
	// +optional
	Detail string `json:"detail,omitempty"`
}

const (
	TemplateErrMissingVar   = "MissingVariable"
	TemplateErrBadOverride  = "UnknownPropertyOverride"
	TemplateErrNameConflict = "NameConflictsWithUnmanagedDevice"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dt;template,scope=Namespaced
// +kubebuilder:printcolumn:name="Instances",type=integer,JSONPath=`.status.instances`
// +kubebuilder:printcolumn:name="Created",type=integer,JSONPath=`.status.created`
// +kubebuilder:printcolumn:name="Orphaned",type=integer,JSONPath=`.status.orphaned`
// +kubebuilder:printcolumn:name="Last-Applied",type=string,JSONPath=`.status.lastApplied`
type DeviceTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceTemplateSpec   `json:"spec,omitempty"`
	Status DeviceTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DeviceTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceTemplate{}, &DeviceTemplateList{})
}
