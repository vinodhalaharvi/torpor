package driver

import (
	"encoding/json"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/kubeedge/mapper-framework/pkg/common"
)

// CustomizedDev is the customized device configuration and client information.
type CustomizedDev struct {
	Instance         common.DeviceInstance
	CustomizedClient *CustomizedClient
}

// CustomizedClient holds one MQTT connection per Device object.
//
// The important design point: MQTT is PUSH. ESPHome publishes on its own
// schedule and nothing can ask it for a reading on demand. So this client
// subscribes once and keeps a cache; GetDeviceData is a cache lookup, not a
// round trip to the device.
//
// That is not a workaround — it is the shape of the whole project. A value
// always has an age, and for a battery node that age is measured in hours.
// `stamps` is where V2's Sleeping / Unreachable distinction will hook in.
type CustomizedClient struct {
	deviceMutex sync.Mutex
	ProtocolConfig

	client mqtt.Client

	cacheMu sync.RWMutex
	values  map[string]string    // topic (or lora field name) -> last payload
	stamps  map[string]time.Time // same key -> when it arrived
	status  string               // last retained payload on the status topic

	// lastFrame is when any LoRa frame from this node last arrived. Unused for
	// directly-addressed devices; for a LoRa node it is the only liveness
	// signal there is.
	lastFrame time.Time
}

type ProtocolConfig struct {
	ProtocolName string `json:"protocolName"`
	ConfigData   `json:"configData"`
}

// ConfigData is per-device, from the Device CR's spec.protocol.configData.
type ConfigData struct {
	// Broker in paho form: tcp://192.168.68.113:1883
	Broker string `json:"broker"`

	// TopicPrefix matches ESPHome's `mqtt.topic_prefix`, e.g. "w10-a".
	// Visitor topics are resolved relative to this.
	TopicPrefix string `json:"topicPrefix"`

	// Optional. ClientID defaults to "torpor-mapper-<topicPrefix>".
	ClientID string `json:"clientID,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// StatusTopic defaults to "<topicPrefix>/status", which is where the
	// ESPHome config publishes a retained birth/will message. That retained
	// LWT is what lets the mapper distinguish "gone" from "just quiet".
	StatusTopic string `json:"statusTopic,omitempty"`

	// ---- LoRa fields. Set these and the device is reached through a gateway
	// rather than addressed directly.

	// Gateway is the topicPrefix of the board relaying for this device, e.g.
	// "w10-a". Its /lora/rx topic carries every frame it hears.
	//
	// This is the field that makes a device with no IP addressable. The Device
	// object points at a gateway; nothing points at the device.
	Gateway string `json:"gateway,omitempty"`

	// NodeID is the sender id in byte 1 of the wire protocol. The whole
	// addressing scheme for the mesh is one byte.
	NodeID int `json:"nodeID,omitempty"`

	// ExpectedIntervalSeconds is how often this node is expected to transmit.
	// Silence shorter than this is normal; silence longer than
	// StaleMultiplier x this is Unreachable.
	//
	// This is the number that distinguishes Sleeping from broken, and it is
	// per-device because a mains gateway and a battery node checking in daily
	// are both healthy at wildly different rates.
	ExpectedIntervalSeconds int `json:"expectedIntervalSeconds,omitempty"`

	// StaleMultiplier defaults to 3. Missing three check-ins is a judgement
	// call, not a fact — which is why it is configurable.
	StaleMultiplier int `json:"staleMultiplier,omitempty"`

	// ConnectTimeoutSeconds bounds the initial connect. Default 10.
	ConnectTimeoutSeconds int `json:"connectTimeoutSeconds,omitempty"`
}

type VisitorConfig struct {
	ProtocolName      string `json:"protocolName"`
	VisitorConfigData `json:"configData"`
}

// VisitorConfigData is per-property, from spec.properties[].visitors.configData.
type VisitorConfigData struct {
	// DataType: int, float, double, boolean, string. Drives conversion of
	// the raw MQTT payload, which is always text on the wire.
	DataType string `json:"dataType"`

	// Topic relative to TopicPrefix, e.g. "sensor/temperature/state".
	// An absolute topic (one containing "/" at position 0 stripped, or simply
	// set via AbsoluteTopic) is also accepted for odd cases.
	Topic string `json:"topic"`

	// CommandTopic is the write path, used by DeviceDataWrite. Also relative.
	// e.g. "switch/speaker_amp/command"
	CommandTopic string `json:"commandTopic,omitempty"`

	// AbsoluteTopic bypasses TopicPrefix entirely when true.
	AbsoluteTopic bool `json:"absoluteTopic,omitempty"`

	Retain bool `json:"retain,omitempty"`
	QoS    byte `json:"qos,omitempty"`
}

type AnomalyDetectionRequest struct {
	Enabled                bool            `json:"enabled"`
	VisitorConfig          VisitorConfig   `json:"visitorConfig"`
	AnomalyDetectionConfig json.RawMessage `json:"anomalyDetectionConfig"`
	Data                   interface{}     `json:"data"`
}
