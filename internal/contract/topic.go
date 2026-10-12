// Package contract holds the wire contract shared by the simulator (stand-in
// CX5240/TF6701) and the ingest service: topic layout, payload types and JSON
// Schema validation. Both sides use it so the PoC proves the exact format the
// PLC will speak.
package contract

import (
	"fmt"
	"strings"
)

// TopicRoot is the fixed prefix of every topic in this PoC: rain/pts/...
const TopicRoot = "rain/pts"

// Kind is the payload class carried by the last topic segment.
type Kind string

const (
	KindTelemetry Kind = "telemetry"
	KindStatus    Kind = "status"
	KindAlarm     Kind = "alarm"
)

// Kinds lists every payload kind, in subscription order.
var Kinds = []Kind{KindTelemetry, KindStatus, KindAlarm}

// Class is the device class in the third topic segment.
type Class string

const (
	ClassEMS     Class = "ems"
	ClassPV      Class = "pv"
	ClassBESS    Class = "bess"
	ClassMeter   Class = "meter"
	ClassWeather Class = "weather"
	ClassBreaker Class = "breaker"
	ClassGen     Class = "gen"
)

// Classes lists every device class allowed in a topic.
var Classes = []Class{ClassEMS, ClassPV, ClassBESS, ClassMeter, ClassWeather, ClassBreaker, ClassGen}

// PublisherID is the single physical MQTT client: TF6701 on the CX5240
// publishes on behalf of every device. clientId == username.
const PublisherID = "ems-pv-01"

// LWTTopic is where the broker publishes the will message, retained.
const LWTTopic = TopicRoot + "/ems/" + PublisherID + "/status"

// LWTPayload is the will payload. It is frozen at CONNECT time and therefore
// cannot carry a live ts, which is why the status schema only requires online.
var LWTPayload = []byte(`{"online":false}`)

// Topic is a parsed rain/pts/{class}/{deviceId}/{kind} topic.
type Topic struct {
	Class    Class
	DeviceID string
	Kind     Kind
}

// String renders the topic in wire form.
func (t Topic) String() string {
	return fmt.Sprintf("%s/%s/%s/%s", TopicRoot, t.Class, t.DeviceID, t.Kind)
}

// ParseTopic splits a topic and rejects anything outside the contract.
func ParseTopic(topic string) (Topic, error) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 || parts[0] != "rain" || parts[1] != "pts" {
		return Topic{}, fmt.Errorf("topic %q does not match %s/{class}/{deviceId}/{kind}", topic, TopicRoot)
	}
	class, deviceID, kind := Class(parts[2]), parts[3], Kind(parts[4])
	if !validClass(class) {
		return Topic{}, fmt.Errorf("topic %q has unknown class %q", topic, class)
	}
	if !validKind(kind) {
		return Topic{}, fmt.Errorf("topic %q has unknown kind %q", topic, kind)
	}
	if deviceID == "" {
		return Topic{}, fmt.Errorf("topic %q has an empty deviceId", topic)
	}
	return Topic{Class: class, DeviceID: deviceID, Kind: kind}, nil
}

// SharedSubscription returns the $share subscription ingest uses for a kind, so
// extra ingest instances split the load instead of duplicating it.
func SharedSubscription(group string, kind Kind) string {
	return fmt.Sprintf("$share/%s/%s/+/+/%s", group, TopicRoot, kind)
}

func validClass(c Class) bool {
	for _, known := range Classes {
		if c == known {
			return true
		}
	}
	return false
}

func validKind(k Kind) bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}
