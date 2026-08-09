package main

import (
	"time"

	"github.com/sixxhands/fyrwire/internal/daemon/agent"
	"github.com/sixxhands/fyrwire/internal/daemon/notify"
	"github.com/sixxhands/fyrwire/internal/network/provider"
	_ "github.com/sixxhands/fyrwire/internal/network/provider/backends"
	"github.com/sixxhands/fyrwire/internal/network/relay"
	"github.com/sixxhands/fyrwire/internal/network/source"
	_ "github.com/sixxhands/fyrwire/internal/network/source/gateway"
	_ "github.com/sixxhands/fyrwire/internal/network/source/manual"
	"github.com/sixxhands/fyrwire/internal/node/config"
)

// Assembles the whole node from blueprints and factories. If anything refuses to start, bail before touching DNS.
func buildAgent(configPath, statePath string) (*agent.Agent, *config.ConfigData, error) {
	loaded, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}

	portSource, err := buildPortSource(loaded.Source)
	if err != nil {
		return nil, nil, err
	}

	dnsProvider, err := buildDnsProvider(loaded.Provider)
	if err != nil {
		return nil, nil, err
	}

	nodePlan, err := loaded.Plan()
	if err != nil {
		return nil, nil, err
	}

	var observers []agent.IAgentObserver
	if notifier := buildNotifier(loaded); notifier != nil {
		observers = append(observers, alertObserver{notifier: notifier, nodeName: loaded.Node.AName})
	}

	options := agent.OptionsData{
		Relay:      loaded.Node.Relay,
		RelayUdp:   loaded.Node.RelayUdp,
		BindHost:   loaded.Node.BindHost,
		TargetHost: loaded.Node.TargetHost,
		Log:        logLine,
		Udp:        buildUdpOptions(loaded.Node.Udp),
		Observers:  observers,
		NodeName:   loaded.Node.AName,
	}

	blueprint, err := agent.CreateAgentBlueprint(portSource, dnsProvider, nodePlan, statePath, options)
	if err != nil {
		return nil, nil, err
	}

	return agent.AgentFactory{}.Create(blueprint), loaded, nil
}

func buildPortSource(section config.Section) (source.IPortSource, error) {
	blueprint, err := source.CreateSourceBlueprint(section, logLine)
	if err != nil {
		return nil, err
	}

	return source.SourceFactory{}.Create(blueprint)
}

func buildDnsProvider(section config.Section) (provider.IDnsProvider, error) {
	blueprint, err := provider.CreateProviderBlueprint(section)
	if err != nil {
		return nil, err
	}

	return provider.ProviderFactory{}.Create(blueprint)
}

func buildNotifier(loaded *config.ConfigData) *notify.Notifier {
	blueprint, err := notify.CreateNotifierBlueprint(
		loaded.Alerts.Webhook,
		loaded.Alerts.Command,
		loaded.Alerts.AfterFailures,
		0,
		logLine,
	)
	if err != nil {
		return nil
	}

	return notify.NotifierFactory{}.Create(blueprint)
}

func buildUdpOptions(udpConfig config.UdpConfigData) relay.UdpOptionsData {
	options := relay.UdpOptionsData{DropZeroLength: true}
	if udpConfig.DropZeroLength != nil {
		options.DropZeroLength = *udpConfig.DropZeroLength
	}

	if udpConfig.MaxSessions != nil {
		options.MaxSessions = *udpConfig.MaxSessions
		options.MaxSessionsSet = true
	}

	if udpConfig.MaxPerIp != nil {
		options.MaxPerIp = *udpConfig.MaxPerIp
		options.MaxPerIpSet = true
	}

	if udpConfig.NewPerSec != nil {
		options.NewPerSec = *udpConfig.NewPerSec
		options.NewPerSecSet = true
	}

	if udpConfig.IdleNew != nil {
		options.IdleNew = time.Duration(*udpConfig.IdleNew) * time.Second
		options.IdleNewSet = true
	}

	if udpConfig.IdleEstablished != nil {
		options.IdleEstablished = time.Duration(*udpConfig.IdleEstablished) * time.Second
		options.IdleEstablishedSet = true
	}

	if udpConfig.ReapInterval != nil {
		options.ReapInterval = time.Duration(*udpConfig.ReapInterval * float64(time.Second))
	}

	return options
}
