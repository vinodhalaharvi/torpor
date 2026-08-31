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

	// lastSeenPer records the last traffic on each transport by name, which is
	// what lets a transport go stale independently of the device.
	//
	// A device is not up or down. Each of its doors is, separately, and the
	// answer to "can I send this" depends on which door and how big the thing
	// is.
	lastSeenPer map[string]time.Time
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

	// Transports declares every way this device can be reached, in priority
	// order. When set it supersedes the single-transport fields below.
	//
	// This is what makes the transport ladder real. A node with LoRa always
	// and WiFi opportunistically is one device with two doors, not two devices
	// — and which door is open changes during the day without anything about
	// the device changing.
	//
	// Config goes out over LoRa tonight. Firmware waits for WiFi.
	Transports []TransportConfig `json:"transports,omitempty"`

	// ---- Single-transport fields, kept for the common case and for the
	// devices already deployed against them.

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

// TransportConfig is one way of reaching a device.
//
// Two shapes. A directly-addressed transport has a topicPrefix — the device
// has topics of its own. A relayed transport has a gateway and a nodeID, and
// the device has no topics anywhere: it exists only inside frames republished
// by something else.
type TransportConfig struct {
	// Type: wifi, lora, thread, ble. Matched against the controller's
	// capability table, so the names have to agree.
	Type string `json:"type"`

	// Directly addressed: the device's own MQTT prefix.
	TopicPrefix string `json:"topicPrefix,omitempty"`

	// Relayed: whose /lora/rx to listen on, and which sender id is ours.
	Gateway string `json:"gateway,omitempty"`
	NodeID  int    `json:"nodeID,omitempty"`

	// OTA declares whether this transport can carry firmware. Not discovered
	// by trying — the whole point is to know before attempting.
	OTA bool `json:"ota,omitempty"`

	// Config declares whether it can carry a property write. Nearly always
	// true; a few bytes fit through almost anything.
	Config bool `json:"config,omitempty"`

	// MaxWriteBytes bounds a single write on this transport. A 240-byte LoRa
	// frame cannot carry a firmware URL plus a hash, and finding that out by
	// truncating the payload is worse than refusing.
	MaxWriteBytes int `json:"maxWriteBytes,omitempty"`

	// StaleAfterSeconds is how long silence on this transport means it is no
	// longer up.
	//
	// This is the field that makes reachability decay. Without it a transport
	// that was once seen is believed forever, and a planner cheerfully routes
	// firmware to a WiFi link that has been down since Tuesday.
	StaleAfterSeconds int `json:"staleAfterSeconds,omitempty"`
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

	// RequiresTransport pins this property to a named transport. Used for
	// firmware_url, which must not be attempted over a transport that cannot
	// carry it — the refusal has to happen at write time as well as at
	// planning time, because a plan can be stale by the time it is acted on.
	RequiresTransport string `json:"requiresTransport,omitempty"`

	// MinBytes is how large a write on this property is. Compared against a
	// transport's MaxWriteBytes before choosing it.
	MinBytes int `json:"minBytes,omitempty"`

	Retain bool `json:"retain,omitempty"`
	QoS    byte `json:"qos,omitempty"`
}

type AnomalyDetectionRequest struct {
	Enabled                bool            `json:"enabled"`
	VisitorConfig          VisitorConfig   `json:"visitorConfig"`
	AnomalyDetectionConfig json.RawMessage `json:"anomalyDetectionConfig"`
	Data                   interface{}     `json:"data"`
}
