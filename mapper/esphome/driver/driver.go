package driver

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"k8s.io/klog/v2"

	"github.com/kubeedge/mapper-framework/pkg/common"
)

func NewClient(protocol ProtocolConfig) (*CustomizedClient, error) {
	client := &CustomizedClient{
		ProtocolConfig: protocol,
		deviceMutex:    sync.Mutex{},
		values:         make(map[string]string),
		stamps:         make(map[string]time.Time),
		status:         common.DeviceStatusUnknown,
	}
	return client, nil
}

// statusTopic returns the retained birth/will topic for this device.
func (c *CustomizedClient) statusTopic() string {
	if c.StatusTopic != "" {
		return c.StatusTopic
	}
	return c.TopicPrefix + "/status"
}

// resolve turns a visitor's relative topic into a full topic.
func (c *CustomizedClient) resolve(v *VisitorConfig, topic string) string {
	if topic == "" {
		return ""
	}
	if v.AbsoluteTopic {
		return topic
	}
	return c.TopicPrefix + "/" + strings.TrimPrefix(topic, "/")
}

// InitDevice connects to the broker and subscribes to everything under this
// device's prefix.
//
// One wildcard subscription rather than one per property: ESPHome publishes a
// couple of dozen topics per board, the payloads are tiny, and a single
// subscription means adding a property to the DeviceModel needs no change
// here. The cost is holding a few dozen strings in memory.
func (c *CustomizedClient) InitDevice() error {
	if c.Broker == "" {
		return fmt.Errorf("esphome-mqtt: broker is required in protocol configData")
	}
	if c.TopicPrefix == "" {
		return fmt.Errorf("esphome-mqtt: topicPrefix is required in protocol configData")
	}

	clientID := c.ClientID
	if clientID == "" {
		clientID = "torpor-mapper-" + c.TopicPrefix
	}

	opts := mqtt.NewClientOptions().
		AddBroker(c.Broker).
		SetClientID(clientID).
		SetAutoReconnect(true).
		SetCleanSession(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second)

	if c.Username != "" {
		opts.SetUsername(c.Username)
		opts.SetPassword(c.Password)
	}

	// Re-subscribe on every (re)connect. AutoReconnect restores the TCP
	// session but not the subscriptions when CleanSession is true.
	opts.SetOnConnectHandler(func(cl mqtt.Client) {
		filter := c.TopicPrefix + "/#"
		if token := cl.Subscribe(filter, 0, c.onMessage); token.Wait() && token.Error() != nil {
			klog.Errorf("esphome-mqtt: subscribe %s failed: %v", filter, token.Error())
			return
		}
		klog.Infof("esphome-mqtt: connected to %s, subscribed to %s", c.Broker, filter)
	})

	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		klog.Warningf("esphome-mqtt: connection to %s lost: %v", c.Broker, err)
		// Deliberately NOT clearing the cache. Losing the broker says nothing
		// about the device; the last known value stays valid, just older.
		c.cacheMu.Lock()
		c.status = common.DeviceStatusUnknown
		c.cacheMu.Unlock()
	})

	c.client = mqtt.NewClient(opts)

	timeout := time.Duration(c.ConnectTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	token := c.client.Connect()
	if !token.WaitTimeout(timeout) {
		return fmt.Errorf("esphome-mqtt: timed out connecting to %s after %s", c.Broker, timeout)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("esphome-mqtt: connect to %s: %w", c.Broker, err)
	}
	return nil
}

// onMessage caches every payload under this device's prefix, with its arrival
// time. The status topic is tracked separately since it drives device state.
func (c *CustomizedClient) onMessage(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	payload := string(msg.Payload())

	c.cacheMu.Lock()
	c.values[topic] = payload
	c.stamps[topic] = time.Now()
	if topic == c.statusTopic() {
		c.status = payload
	}
	c.cacheMu.Unlock()

	klog.V(4).Infof("esphome-mqtt: %s = %s", topic, payload)
}

// GetDeviceData returns the last value seen for the visitor's topic.
//
// It does not go to the device. There is nothing to ask — ESPHome pushes on
// its own schedule. An empty cache means nothing has arrived yet, which is a
// real error at V0 (the board is on WiFi and should be publishing) but will be
// an ordinary condition at V2 (the node is asleep).
func (c *CustomizedClient) GetDeviceData(visitor *VisitorConfig) (interface{}, error) {
	topic := c.resolve(visitor, visitor.Topic)
	if topic == "" {
		return nil, fmt.Errorf("esphome-mqtt: visitor has no topic")
	}

	c.cacheMu.RLock()
	raw, ok := c.values[topic]
	age := time.Since(c.stamps[topic])
	c.cacheMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("esphome-mqtt: no value yet for %s", topic)
	}
	klog.V(4).Infof("esphome-mqtt: read %s = %s (age %s)", topic, raw, age.Round(time.Second))

	return convert(raw, visitor.DataType)
}

// convert parses the text payload according to the property's declared type.
// MQTT is untyped on the wire; the DeviceModel is where type lives.
func convert(raw string, dataType string) (interface{}, error) {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(dataType) {
	case "int", "integer", "int64":
		// ESPHome sometimes publishes an integer-valued sensor as "14.00".
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), nil
		}
		return nil, fmt.Errorf("esphome-mqtt: %q is not an int", s)
	case "float", "double", "float64":
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("esphome-mqtt: %q is not a float", s)
		}
		return f, nil
	case "boolean", "bool":
		switch strings.ToUpper(s) {
		case "ON", "TRUE", "1":
			return true, nil
		case "OFF", "FALSE", "0":
			return false, nil
		}
		return nil, fmt.Errorf("esphome-mqtt: %q is not a boolean", s)
	default:
		return s, nil
	}
}

// DeviceDataWrite publishes to the property's command topic. This is V1.
func (c *CustomizedClient) DeviceDataWrite(visitor *VisitorConfig, deviceMethodName string, propertyName string, data interface{}) error {
	topic := c.resolve(visitor, visitor.CommandTopic)
	if topic == "" {
		return fmt.Errorf("esphome-mqtt: property %q has no commandTopic; it is read-only", propertyName)
	}

	payload := format(data, visitor.DataType)

	c.deviceMutex.Lock()
	defer c.deviceMutex.Unlock()

	token := c.client.Publish(topic, visitor.QoS, visitor.Retain, payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("esphome-mqtt: timed out publishing to %s", topic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("esphome-mqtt: publish to %s: %w", topic, err)
	}
	klog.V(3).Infof("esphome-mqtt: wrote %s = %s", topic, payload)
	return nil
}

// format renders a value the way ESPHome expects it on a command topic.
// Switches want ON/OFF, not true/false.
func format(data interface{}, dataType string) string {
	switch v := data.(type) {
	case bool:
		if v {
			return "ON"
		}
		return "OFF"
	case string:
		switch strings.ToUpper(strings.TrimSpace(v)) {
		case "TRUE":
			return "ON"
		case "FALSE":
			return "OFF"
		}
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (c *CustomizedClient) SetDeviceData(data interface{}, visitor *VisitorConfig) error {
	return c.DeviceDataWrite(visitor, "", "", data)
}

func (c *CustomizedClient) StopDevice() error {
	if c.client != nil && c.client.IsConnected() {
		c.client.Disconnect(250)
	}
	return nil
}

// GetDeviceStates reports device liveness from the retained birth/will
// message, not from whether the mapper's own broker connection is up.
//
// The distinction matters. A dead broker means the mapper is blind, not that
// the device is down — those are different facts and conflating them is how
// fleet tools end up paging someone about a node that is fine.
//
// This is also the seam for V2. Right now "offline" is the only negative
// state ESPHome can express. A LoRa node that has not checked in is neither
// online nor offline — it is Sleeping, and that will need a state this
// function cannot currently return.
func (c *CustomizedClient) GetDeviceStates() (string, error) {
	if c.client == nil || !c.client.IsConnected() {
		return common.DeviceStatusUnknown, nil
	}

	c.cacheMu.RLock()
	status := c.status
	c.cacheMu.RUnlock()

	switch strings.ToLower(strings.TrimSpace(status)) {
	case "online":
		return common.DeviceStatusOK, nil
	case "offline":
		return common.DeviceStatusOffline, nil
	default:
		return common.DeviceStatusUnknown, nil
	}
}

func (c *CustomizedClient) AnomalyDetectionProcess(req *AnomalyDetectionRequest) error {
	return nil
}
