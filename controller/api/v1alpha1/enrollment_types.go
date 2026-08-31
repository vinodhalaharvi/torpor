package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceEnrollment turns a device that announced itself into a device the
// fleet knows about.
//
// Everything else in this project assumes devices already exist, and that
// assumption does a lot of quiet work. Somebody typed a Device manifest. For
// three boards on a desk that is fine; for fifteen arriving in a box it is two
// hours of transcription, and transcription is where node ids get swapped.
//
// The shape is a certificate signing request, and deliberately so. A device
// announcing "I am field-01 on gateway gw-north as node 2" is making a
// *claim*, not stating a fact — anyone who can reach the broker can announce
// anything. Kubernetes already has the right pattern for this: the claim
// arrives, something approves it, and only then does the identity exist.
//
// What this does NOT do is create identity. First flash is physical, over USB,
// once per device, and that is not a limitation to engineer around: a device
// that can be fully provisioned over the air by anyone is a device anyone can
// steal.
type DeviceEnrollmentSpec struct {
	// AnnounceTopic is where devices publish their claims. Retained, so a
	// device that announced before the controller started is not lost.
	// +optional
	AnnounceTopic string `json:"announceTopic,omitempty"`

	// Broker to watch. Empty means the same broker the mapper uses.
	// +optional
	Broker string `json:"broker,omitempty"`

	// AutoApprove creates Devices without a human.
	//
	// Default false, and it should stay false anywhere that matters. An
	// announcement is a claim from an unauthenticated source; auto-approving
	// means anybody who can reach the broker can add devices to your fleet and
	// have your rollouts push firmware at them.
	//
	// True is reasonable on a bench, and on a bench only.
	// +optional
	AutoApprove bool `json:"autoApprove,omitempty"`

	// RequireTemplate only approves devices whose name appears as an instance
	// in the named DeviceTemplate.
	//
	// This is the useful middle ground and probably the right default for a
	// real deployment. You already wrote down which fifteen devices you
	// ordered; a device claiming to be one of them is checkable against that
	// list, and a device claiming to be something else is not something you
	// want to find out about by discovering it in a rollout.
	// +optional
	RequireTemplate string `json:"requireTemplate,omitempty"`

	// AllowedModels restricts which hardware may enroll. A claim to be a model
	// you have never bought is worth rejecting on its own.
	// +optional
	AllowedModels []string `json:"allowedModels,omitempty"`

	// Labels applied to every enrolled device.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// NodeName the created Devices are bound to.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// ExpirePendingAfter drops unapproved announcements.
	//
	// Without this, a device announced once during a bench test lingers as a
	// pending row forever, and a pending list nobody has cleared is a pending
	// list nobody reads.
	// +optional
	ExpirePendingAfter *metav1.Duration `json:"expirePendingAfter,omitempty"`
}

// DeviceAnnouncement is what a device claimed about itself.
//
// Every field is a claim. The naming is deliberate — treating this as observed
// truth is precisely the mistake the approval step exists to prevent.
type DeviceAnnouncement struct {
	Device string `json:"device"`

	// +optional
	Model string `json:"model,omitempty"`
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// +optional
	NodeID int `json:"nodeID,omitempty"`
	// +optional
	TopicPrefix string `json:"topicPrefix,omitempty"`
	// +optional
	Transports []string `json:"transports,omitempty"`
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// FirmwareVersion and BuildTime, useful for spotting a device flashed from
	// a stale build before it ever joins — the failure that cost an hour on
	// real hardware here.
	// +optional
	FirmwareVersion string `json:"firmwareVersion,omitempty"`
	// +optional
	BuildTime string `json:"buildTime,omitempty"`

	// +optional
	AnnouncedAt *metav1.Time `json:"announcedAt,omitempty"`
	// +optional
	LastAnnouncedAt *metav1.Time `json:"lastAnnouncedAt,omitempty"`

	// Conflicts lists reasons this claim is suspicious. Populated rather than
	// acted on, because the interesting cases want a human: two devices
	// claiming the same node id behind one gateway is either a
	// misconfiguration or somebody's idea of a joke, and both deserve looking
	// at rather than silent rejection.
	// +optional
	Conflicts []string `json:"conflicts,omitempty"`
}

type DeviceEnrollmentStatus struct {
	// Pending are claims awaiting approval.
	// +optional
	Pending []DeviceAnnouncement `json:"pending,omitempty"`

	// Rejected, with the reason. Kept rather than dropped: a device rejected
	// for claiming an unknown model is a device somebody should look at, and
	// deleting the record makes the question unanswerable.
	// +optional
	Rejected []DeviceAnnouncement `json:"rejected,omitempty"`

	// +optional
	Enrolled int32 `json:"enrolled,omitempty"`
	// +optional
	PendingCount int32 `json:"pendingCount,omitempty"`
	// +optional
	RejectedCount int32 `json:"rejectedCount,omitempty"`

	// +optional
	LastAnnouncement *metav1.Time `json:"lastAnnouncement,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const (
	EnrollRejectUnknownModel  = "ModelNotAllowed"
	EnrollRejectNotInTemplate = "NotInTemplate"
	EnrollRejectNameTaken     = "NameAlreadyEnrolled"
	EnrollRejectNodeIDTaken   = "NodeIDConflictOnGateway"
	EnrollRejectDecommissioned = "DeviceWasDecommissioned"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=enroll,scope=Namespaced
// +kubebuilder:printcolumn:name="Enrolled",type=integer,JSONPath=`.status.enrolled`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.pendingCount`
// +kubebuilder:printcolumn:name="Rejected",type=integer,JSONPath=`.status.rejectedCount`
// +kubebuilder:printcolumn:name="Auto",type=boolean,JSONPath=`.spec.autoApprove`,priority=1
type DeviceEnrollment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceEnrollmentSpec   `json:"spec,omitempty"`
	Status DeviceEnrollmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DeviceEnrollmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceEnrollment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceEnrollment{}, &DeviceEnrollmentList{})
}
