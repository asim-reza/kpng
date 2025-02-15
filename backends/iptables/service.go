/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package iptables

import (
	"fmt"
	"net"
	"strings"

	"sigs.k8s.io/kpng/backends/iptables/util"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	//"k8s.io/kubernetes/pkg/proxy/metrics"

	localnetv1 "sigs.k8s.io/kpng/api/localnetv1"
)

// BaseServiceInfo contains base information that defines a service.
// This could be used directly by proxier while processing services,
// or can be used for constructing a more specific ServiceInfo struct
// defined by the proxier if needed.
type BaseServiceInfo struct {
	clusterIP                net.IP
	port                     int
	protocol                 localnetv1.Protocol
	nodePort                 int
	loadBalancerIPs          []string
	sessionAffinity          SessionAffinity
	stickyMaxAgeSeconds      int
	externalIPs              []string
	loadBalancerSourceRanges []string
	healthCheckNodePort      int
	nodeLocalExternal        bool
	nodeLocalInternal        bool
	internalTrafficPolicy    *v1.ServiceInternalTrafficPolicyType
	hintsAnnotation          string
	targetPort               int
	targetPortName           string
	portName                 string
}

// SessionAffinity contains data about assinged session affinity
type SessionAffinity struct {
	ClientIP *localnetv1.Service_ClientIP
}

var _ ServicePort = &BaseServiceInfo{}

// String is part of ServicePort interface.
func (info *BaseServiceInfo) String() string {
	return fmt.Sprintf("%s:%d/%s", info.clusterIP, info.port, info.protocol)
}

// ClusterIP is part of ServicePort interface.
func (info *BaseServiceInfo) ClusterIP() net.IP {
	return info.clusterIP
}

// Port is part of ServicePort interface.
func (info *BaseServiceInfo) Port() int {
	return info.port
}

// Port is part of ServicePort interface.
func (info *BaseServiceInfo) TargetPort() int {
	return info.targetPort
}

// PortName is part of ServicePort interface.
func (info *BaseServiceInfo) PortName() string {
	return info.portName
}

func (info *BaseServiceInfo) TargetPortName() string {
	return info.targetPortName
}

// SessionAffinity is part of the ServicePort interface.
func (info *BaseServiceInfo) SessionAffinity() SessionAffinity {
	return info.sessionAffinity
}

// Protocol is part of ServicePort interface.
func (info *BaseServiceInfo) Protocol() localnetv1.Protocol {
	return info.protocol
}

// LoadBalancerSourceRanges is part of ServicePort interface
func (info *BaseServiceInfo) LoadBalancerSourceRanges() []string {
	return info.loadBalancerSourceRanges
}

// HealthCheckNodePort is part of ServicePort interface.
func (info *BaseServiceInfo) HealthCheckNodePort() int {
	return info.healthCheckNodePort
}

// NodePort is part of the ServicePort interface.
func (info *BaseServiceInfo) NodePort() int {
	return info.nodePort
}

// ExternalIPStrings is part of ServicePort interface.
func (info *BaseServiceInfo) ExternalIPStrings() []string {
	return info.externalIPs
}

// LoadBalancerIPStrings is part of ServicePort interface.
func (info *BaseServiceInfo) LoadBalancerIPStrings() []string {
	var ips []string
	for _, ing := range info.loadBalancerIPs {
		ips = append(ips, ing)
	}
	return ips
}

// NodeLocalExternal is part of ServicePort interface.
func (info *BaseServiceInfo) NodeLocalExternal() bool {
	return info.nodeLocalExternal
}

// NodeLocalInternal is part of ServicePort interface
func (info *BaseServiceInfo) NodeLocalInternal() bool {
	return info.nodeLocalInternal
}

// InternalTrafficPolicy is part of ServicePort interface
func (info *BaseServiceInfo) InternalTrafficPolicy() *v1.ServiceInternalTrafficPolicyType {
	return info.internalTrafficPolicy
}

// HintsAnnotation is part of ServicePort interface.
func (info *BaseServiceInfo) HintsAnnotation() string {
	return info.hintsAnnotation
}

func (sct *ServiceChangeTracker) newBaseServiceInfo(port *localnetv1.PortMapping, service *localnetv1.Service) *BaseServiceInfo {
	nodeLocalExternal := false
	if RequestsOnlyLocalTraffic(service) {
		nodeLocalExternal = true
	}
	nodeLocalInternal := false
	//TODO : CHECK InternalTrafficPolicy
	// if utilfeature.DefaultFeatureGate.Enabled(features.ServiceInternalTrafficPolicy) {
	// 	nodeLocalInternal = apiservice.RequestsOnlyLocalTrafficForInternal(service)
	// }

	clusterIP := GetClusterIPByFamily(sct.ipFamily, service)
	info := &BaseServiceInfo{
		clusterIP:         net.ParseIP(clusterIP),
		port:              int(port.Port),
		portName:          port.Name,
		targetPort:        int(port.TargetPort),
		targetPortName:    port.TargetPortName,
		protocol:          port.Protocol,
		nodePort:          int(port.NodePort),
		nodeLocalExternal: nodeLocalExternal,
		nodeLocalInternal: nodeLocalInternal,
		// internalTrafficPolicy: service.Spec.InternalTrafficPolicy, //TODO : CHECK InternalTrafficPolicy
		hintsAnnotation:          service.Annotations[v1.AnnotationTopologyAwareHints],
		loadBalancerSourceRanges: getLoadbalancerSourceRanges(service.IPFilters),
		loadBalancerIPs:          getLoadBalancerIPs(service.IPs.LoadBalancerIPs, sct.ipFamily),
		sessionAffinity:          getSessionAffinity(service.SessionAffinity),
	}

	// filter external ips, source ranges and ingress ips
	// prior to dual stack services, this was considered an error, but with dual stack
	// services, this is actually expected. Hence we downgraded from reporting by events
	// to just log lines with high verbosity

	ipFamilyMap := MapIPsByIPFamily(service.IPs.ExternalIPs)
	info.externalIPs = ipFamilyMap[sct.ipFamily]

	// Log the IPs not matching the ipFamily
	if ips, ok := ipFamilyMap[OtherIPFamily(sct.ipFamily)]; ok && len(ips) > 0 {
		klog.V(4).Infof("service change tracker(%v) ignored the following external IPs(%s) for service %v/%v as they don't match IPFamily", sct.ipFamily, strings.Join(ips, ","), service.Namespace, service.Name)
	}

	//TODO : CHECK service.Spec.HealthCheckNodePort
	// if apiservice.NeedsHealthCheck(service) {
	// 	p := service.Spec.HealthCheckNodePort
	// 	if p == 0 {
	// 		klog.Errorf("Service %s/%s has no healthcheck nodeport", service.Namespace, service.Name)
	// 	} else {
	// 		info.healthCheckNodePort = int(p)
	// 	}
	// }

	return info
}

func getSessionAffinity(affinity interface{}) SessionAffinity {
	var sessionAffinity SessionAffinity
	switch affinity.(type) {
	case *localnetv1.Service_ClientIP:
		sessionAffinity.ClientIP = affinity.(*localnetv1.Service_ClientIP)
	}
	return sessionAffinity
}

func getLoadBalancerIPs(ips *localnetv1.IPSet, ipFamily v1.IPFamily) []string {
	if ips == nil {
		return nil
	}
	if ipFamily == v1.IPv4Protocol {
		return ips.V4
	}
	return ips.V6

}

//TODO: Would be better to have SourceRanges also as IPSet instead?
//Change the code to return based on ipfamily once that is done.
func getLoadbalancerSourceRanges(filters []*localnetv1.IPFilter) []string {
	var sourceRanges []string
	for _, filter := range filters {
		if len(filter.SourceRanges) <= 0 {
			continue
		}
		sourceRanges = append(sourceRanges, filter.SourceRanges...)
	}
	return sourceRanges
}

// returns a new ServicePort which abstracts a serviceInfo
func newServiceInfo(port *localnetv1.PortMapping, service *localnetv1.Service, baseInfo *BaseServiceInfo) ServicePort {
	info := &serviceInfo{BaseServiceInfo: baseInfo}

	// Store the following for performance reasons.
	svcName := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
	svcPortName := ServicePortName{
		svcName,
		port.Name,
		info.protocol,
	}
	protocol := strings.ToLower(string(info.Protocol()))
	info.serviceNameString = svcPortName.String()
	info.servicePortChainName = servicePortChainName(info.serviceNameString, protocol)
	info.serviceFirewallChainName = serviceFirewallChainName(info.serviceNameString, protocol)
	info.serviceLBChainName = serviceLBChainName(info.serviceNameString, protocol)

	return info
}

type makeServicePortFunc func(*localnetv1.PortMapping, *localnetv1.Service, *BaseServiceInfo) ServicePort

// This handler is invoked by the apply function on every change. This function should not modify the
// ServiceMap's but just use the changes for any Proxier specific cleanup.
// type processServiceMapChangeFunc func(previous, current ServiceMap)

// serviceChange contains all changes to services that happened since proxy rules were synced.  For a single object,
// changes are accumulated, i.e. previous is state from before applying the changes,
// current is state after applying all of the changes.
// type serviceChange ServiceMap

// ServiceChangeTracker carries state about uncommitted changes to an arbitrary number of
// Services, keyed by their namespace and name.
type ServiceChangeTracker struct {
	// items maps a service to its serviceChange.
	items map[types.NamespacedName]*serviceChange
	// makeServiceInfo allows proxier to inject customized information when processing service.
	makeServiceInfo makeServicePortFunc
	// processServiceMapChange processServiceMapChangeFunc
	ipFamily v1.IPFamily

	recorder events.EventRecorder
}

// NewServiceChangeTracker initializes a ServiceChangeTracker
func NewServiceChangeTracker(makeServiceInfo makeServicePortFunc, ipFamily v1.IPFamily, recorder events.EventRecorder) *ServiceChangeTracker {
	return &ServiceChangeTracker{
		items:           make(map[types.NamespacedName]*serviceChange),
		makeServiceInfo: makeServiceInfo,
		recorder:        recorder,
		ipFamily:        ipFamily,
		// processServiceMapChange: processServiceMapChange,
	}
}

// Update updates given service's change map based on the <previous, current> service pair.  It returns true if items changed,
// otherwise return false.  Update can be used to add/update/delete items of ServiceChangeMap.  For example,
// Add item
//   - pass <nil, service> as the <previous, current> pair.
// Update item
//   - pass <oldService, service> as the <previous, current> pair.
// Delete item
//   - pass <service, nil> as the <previous, current> pair.
func (sct *ServiceChangeTracker) Update(current *localnetv1.Service) bool {
	svc := current
	if svc == nil {
		return false
	}
	//metrics.ServiceChangesTotal.Inc()
	namespacedName := types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}
	var change *serviceChange
	var ok bool
	if change, ok = sct.items[namespacedName]; !ok {
		change = &serviceChange{}
		sct.items[namespacedName] = change
	}
	*change = sct.serviceToServiceMap(current)
	klog.V(2).Infof("Service %s updated: %d ports", namespacedName, len(*change))
	//metrics.ServiceChangesPending.Set(float64(len(sct.items)))
	return len(sct.items) > 0
}

func (sct *ServiceChangeTracker) Delete(namespace, name string) bool {
	//metrics.ServiceChangesTotal.Inc()
	namespacedName := types.NamespacedName{Namespace: namespace, Name: name}
	sct.items[namespacedName] = nil
	klog.V(2).Infof("Service %s updated for delete", namespacedName)
	//metrics.ServiceChangesPending.Set(float64(len(sct.items)))
	return len(sct.items) > 0
}

// UpdateServiceMapResult is the updated results after applying service changes.
type UpdateServiceMapResult struct {
	// HCServiceNodePorts is a map of Service names to node port numbers which indicate the health of that Service on this Node.
	// The value(uint16) of HCServices map is the service health check node port.
	HCServiceNodePorts map[types.NamespacedName]uint16
	// UDPStaleClusterIP holds stale (no longer assigned to a Service) Service IPs that had UDP ports.
	// Callers can use this to abort timeout-waits or clear connection-tracking information.
	UDPStaleClusterIP sets.String
}

// ServiceMap maps a service to its ServicePort.
type serviceChange map[ServicePortName]ServicePort
type ServicesSnapshot map[types.NamespacedName]serviceChange

func (svcSnap *ServicesSnapshot) Update(changes *ServiceChangeTracker) (result UpdateServiceMapResult) {
	result.UDPStaleClusterIP = sets.NewString()
	svcSnap.apply(changes, result.UDPStaleClusterIP)

	// TODO: If this will appear to be computationally expensive, consider
	// computing this incrementally similarly to serviceMap.
	result.HCServiceNodePorts = make(map[types.NamespacedName]uint16)
	for svcPortName, svcPortMap := range *svcSnap {
		for _, svc := range svcPortMap {
			svcInfo, ok := svc.(*serviceInfo)
			if !ok {
				klog.ErrorS(nil, "Failed to cast serviceInfo", "svcName", svcPortName.String())
				continue
			}
			if svcInfo.HealthCheckNodePort() != 0 {
				result.HCServiceNodePorts[svcPortName] = uint16(svcInfo.HealthCheckNodePort())
			}
		}
	}
	return result
}

func (svcSnap *ServicesSnapshot) apply(changes *ServiceChangeTracker, UDPStaleClusterIP sets.String) {
	for svcName, change := range changes.items {
		svcSnap.merge(svcName, change, UDPStaleClusterIP)
	}
	// clear changes after applying them to ServiceMap.
	changes.items = make(map[types.NamespacedName]*serviceChange)
	//metrics.ServiceChangesPending.Set(0)
}

func (svcSnap *ServicesSnapshot) merge(svcName types.NamespacedName, other *serviceChange, UDPStaleClusterIP sets.String) {
	// existingPorts is going to store all identifiers of all services in `other` ServiceMap.
	if other == nil {
		for _, svcInfo := range (*svcSnap)[svcName] {

			if string(svcInfo.Protocol()) == string(v1.ProtocolUDP) {
				UDPStaleClusterIP.Insert(svcInfo.ClusterIP().String())
			}
		}
		delete(*svcSnap, svcName)
		return
	}
	(*svcSnap)[svcName] = *other
}

// internal struct for string service information
type serviceInfo struct {
	*BaseServiceInfo
	// The following fields are computed and stored for performance reasons.
	serviceNameString        string
	servicePortChainName     util.Chain
	serviceFirewallChainName util.Chain
	serviceLBChainName       util.Chain
}

// serviceToServiceMap translates a single Service object to a ServiceMap.
//
// NOTE: service object should NOT be modified.
func (sct *ServiceChangeTracker) serviceToServiceMap(service *localnetv1.Service) serviceChange {
	if service == nil {
		return nil
	}
	clusterIP := GetClusterIPByFamily(sct.ipFamily, service)
	if clusterIP == "" {
		return nil
	}
	serviceMap := make(serviceChange)
	svcName := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
	for i := range service.Ports {
		servicePort := service.Ports[i]
		svcPortName := ServicePortName{NamespacedName: svcName, Port: servicePort.Name, Protocol: servicePort.Protocol}
		baseSvcInfo := sct.newBaseServiceInfo(servicePort, service)
		if sct.makeServiceInfo != nil {
			serviceMap[svcPortName] = sct.makeServiceInfo(servicePort, service, baseSvcInfo)
		} else {
			serviceMap[svcPortName] = baseSvcInfo
		}
	}
	return serviceMap
}

func IsServiceIPSet(service *localnetv1.Service) bool {
	return len(service.IPs.ClusterIPs.V4) > 0 || len(service.IPs.ClusterIPs.V6) > 0
}
