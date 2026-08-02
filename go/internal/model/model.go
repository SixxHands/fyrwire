package model

import (
	"fmt"
	"sort"
	"strings"
)

type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func Errorf(format string, a ...any) error {
	return &Error{Msg: fmt.Sprintf(format, a...)}
}

type PortRequestData struct {
	Key          string
	InternalPort int
	Protocol     string
}

func (p PortRequestData) String() string {
	return fmt.Sprintf("%s(%s/%d)", p.Key, p.Protocol, p.InternalPort)
}

type EndpointData struct {
	Host    string
	Ports   map[string]int
	Partial map[string]bool
}

func (r EndpointData) Endpoint(key string) string {
	return fmt.Sprintf("%s:%d", r.Host, r.Ports[key])
}

func (r EndpointData) Key() string {
	keys := make([]string, 0, len(r.Ports))
	for k := range r.Ports {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, r.Ports[k]))
	}
	return r.Host + "|" + strings.Join(parts, ",")
}

const (
	RecordSRV   = "srv"
	RecordHTTPS = "https"
	RecordNone  = "none"
)

type Service struct {
	Key          string
	InternalPort int
	SrvName      string
	Protocol     string
	Priority     int
	Weight       int
	TTL          int
	Record       string
}

func (s Service) Request() PortRequestData {
	return PortRequestData{Key: s.Key, InternalPort: s.InternalPort, Protocol: s.Protocol}
}

type NodePlanData struct {
	AName    string
	Services []Service
	TTL      int
}

func (n NodePlanData) Requests() []PortRequestData {
	out := make([]PortRequestData, 0, len(n.Services))
	for _, s := range n.Services {
		out = append(out, s.Request())
	}
	return out
}

func (n NodePlanData) Service(key string) (Service, bool) {
	for _, s := range n.Services {
		if s.Key == key {
			return s, true
		}
	}
	return Service{}, false
}

type SrvRecordData struct {
	Name     string
	Target   string
	Port     int
	Priority int
	Weight   int
	TTL      int
}

func (r SrvRecordData) Value() string {
	return fmt.Sprintf("%d %d %d %s", r.Priority, r.Weight, r.Port, r.Target)
}

type HttpsRecordData struct {
	Name     string
	Target   string
	Port     int
	Priority int
	TTL      int
}

func (r HttpsRecordData) Value() string {
	return fmt.Sprintf("%d %s port=%d", r.Priority, r.Target, r.Port)
}
