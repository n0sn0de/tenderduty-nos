package seer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	dash "github.com/n0sn0de/tenderduty-nos/seer/dashboard"
)

type runtimeService interface {
	Name() string
	Addr() net.Addr
	Start() <-chan error
	Shutdown(context.Context) error
	Close() error
}

type dashboardRuntimeService struct {
	listener net.Listener
	server   *dash.Server
}

func (s *dashboardRuntimeService) Name() string   { return "dashboard" }
func (s *dashboardRuntimeService) Addr() net.Addr { return s.listener.Addr() }
func (s *dashboardRuntimeService) Start() <-chan error {
	return s.server.Start(s.listener)
}
func (s *dashboardRuntimeService) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
func (s *dashboardRuntimeService) Close() error {
	return errors.Join(s.listener.Close(), s.server.Close())
}

func listenerAddresses(config *Config) (dashboardAddress, prometheusAddress string, err error) {
	if config.EnableDash {
		dashboardAddress, err = bindAddress("listen_host", config.ListenHost, "listen_port", config.Listen)
		if err != nil {
			return "", "", err
		}
	}
	if config.Prom {
		prometheusAddress, err = bindAddress(
			"prometheus_listen_host",
			config.PrometheusListenHost,
			"prometheus_listen_port",
			strconv.Itoa(config.PrometheusListenPort),
		)
		if err != nil {
			return "", "", err
		}
	}
	return dashboardAddress, prometheusAddress, nil
}

func bindAddress(hostField, host, portField, port string) (string, error) {
	if err := validateBindHost(host); err != nil {
		return "", fmt.Errorf("%s %q is invalid: %w", hostField, host, err)
	}
	if port == "" {
		return "", fmt.Errorf("%s must be a numeric port", portField)
	}
	for _, character := range port {
		if character < '0' || character > '9' {
			return "", fmt.Errorf("%s %q must be a numeric port", portField, port)
		}
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 0 || parsedPort > 65535 {
		return "", fmt.Errorf("%s %q must be between 0 and 65535", portField, port)
	}
	return net.JoinHostPort(host, port), nil
}

func validateBindHost(host string) error {
	if host == "" {
		return nil
	}
	if strings.TrimSpace(host) != host {
		return errors.New("surrounding whitespace is not allowed")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if strings.ContainsAny(host, ":/[]%") {
		return errors.New("provide a host or unbracketed IP address without a scheme, path, zone, or port")
	}
	candidate := strings.TrimSuffix(host, ".")
	if candidate == "" || len(candidate) > 253 {
		return errors.New("hostname length is invalid")
	}
	looksLikeIPv4 := true
	for _, character := range candidate {
		if (character < '0' || character > '9') && character != '.' {
			looksLikeIPv4 = false
			break
		}
	}
	if looksLikeIPv4 && strings.Contains(candidate, ".") {
		return errors.New("malformed IPv4 address")
	}
	for _, label := range strings.Split(candidate, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("hostname label is invalid")
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return errors.New("hostname contains an invalid character")
		}
	}
	return nil
}

func prepareRuntimeServices(config *Config) ([]runtimeService, error) {
	dashboardAddress, prometheusAddress, err := listenerAddresses(config)
	if err != nil {
		return nil, err
	}
	services := make([]runtimeService, 0, 2)
	rollback := func() {
		for _, service := range services {
			_ = service.Close()
		}
	}

	if config.EnableDash {
		listener, listenErr := net.Listen("tcp", dashboardAddress)
		if listenErr != nil {
			return nil, fmt.Errorf("open dashboard listener %s: %w", dashboardAddress, listenErr)
		}
		server, serverErr := dash.NewServer(config.updateChan, config.logChan, config.HideLogs)
		if serverErr != nil {
			_ = listener.Close()
			return nil, fmt.Errorf("prepare dashboard server: %w", serverErr)
		}
		services = append(services, &dashboardRuntimeService{listener: listener, server: server})
	}
	if config.Prom {
		listener, listenErr := net.Listen("tcp", prometheusAddress)
		if listenErr != nil {
			rollback()
			return nil, fmt.Errorf("open Prometheus listener %s: %w", prometheusAddress, listenErr)
		}
		services = append(services, newPrometheusServer(listener, config.statsChan))
	}
	return services, nil
}
