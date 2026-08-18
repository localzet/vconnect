package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"vconnect/relay/egress"
)

type FabricRouteInventory struct {
	SchemaVersion int           `json:"schemaVersion"`
	DefaultRoute  string        `json:"defaultRoute"`
	Routes        []FabricRoute `json:"routes"`
}

type FabricRoute struct {
	ID          string          `json:"id"`
	DisplayName string          `json:"displayName"`
	Region      string          `json:"region"`
	Mode        string          `json:"mode"`
	Auto        bool            `json:"auto"`
	Default     bool            `json:"default"`
	Xray        FabricXrayRoute `json:"xray"`
}

type FabricXrayRoute struct {
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Protocol   string `json:"protocol"`
	Network    string `json:"network"`
	Security   string `json:"security"`
	InboundTag string `json:"inboundTag"`
}

type UserEgressLease interface {
	EgressConfigPath() string
	Close() error
}

type UserEgressFactory interface {
	Acquire(ctx context.Context, userID, requestID string) (UserEgressLease, error)
}

type XrayUserBridgeConfig struct {
	XrayBinary       string
	RoutesPath       string
	WorkDir          string
	IdentityResolver FabricUserIdentityResolver
	ReadyTimeout     time.Duration
}

type XrayUserBridgeFactory struct {
	cfg    XrayUserBridgeConfig
	routes FabricRouteInventory
}

type xrayUserBridgeLease struct {
	cmd        *exec.Cmd
	configPath string
	egressPath string
	dir        string
	once       sync.Once
	closeErr   error
}

func LoadFabricRouteCatalog(path string) (*egress.Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var routes FabricRouteInventory
	if err := json.Unmarshal(raw, &routes); err != nil {
		return nil, err
	}
	if routes.SchemaVersion != 2 || len(routes.Routes) == 0 {
		return nil, errors.New("controlplane: invalid Fabric route catalog")
	}
	profiles := make([]egress.Profile, 0, len(routes.Routes))
	for _, route := range routes.Routes {
		profiles = append(profiles, egress.Profile{
			ID: route.ID, Type: egress.TypeDirect, Enabled: true,
			DisplayName: route.DisplayName, Region: route.Region, Mode: route.Mode, Auto: route.Auto,
		})
	}
	registry, err := egress.RegistryFromConfig(egress.Config{
		SchemaVersion: 1, DefaultEgress: routes.DefaultRoute, Egresses: profiles,
	})
	if err != nil {
		return nil, err
	}
	// Для control call заменяем direct dialers на reachability-probe локальных
	// VLESS ingress'ов. Метаданные сохраняются через повторную сборку ниже.
	dialers := make([]egress.Dialer, 0, len(routes.Routes))
	for _, route := range routes.Routes {
		target := net.JoinHostPort(route.Xray.Address, strconv.Itoa(route.Xray.Port))
		dialers = append(dialers, egress.ReachabilityDialer{ProfileID: route.ID, Target: target})
	}
	probeRegistry, err := egress.NewRegistry(routes.DefaultRoute, dialers...)
	if err != nil {
		return nil, err
	}
	_ = registry // registry валидирует метаданные и schema; descriptors задаются следующим helper'ом.
	return egress.WithDescriptors(probeRegistry, profiles), nil
}

func NewXrayUserBridgeFactory(cfg XrayUserBridgeConfig) (*XrayUserBridgeFactory, error) {
	cfg.XrayBinary = strings.TrimSpace(cfg.XrayBinary)
	cfg.RoutesPath = strings.TrimSpace(cfg.RoutesPath)
	if cfg.XrayBinary == "" {
		return nil, errors.New("controlplane: xray binary is required")
	}
	if cfg.RoutesPath == "" {
		return nil, errors.New("controlplane: Fabric routes path is required")
	}
	if cfg.IdentityResolver == nil {
		return nil, errors.New("controlplane: Fabric identity resolver is required")
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = os.TempDir()
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 8 * time.Second
	}

	raw, err := os.ReadFile(cfg.RoutesPath)
	if err != nil {
		return nil, fmt.Errorf("controlplane: read Fabric routes: %w", err)
	}
	var routes FabricRouteInventory
	if err := json.Unmarshal(raw, &routes); err != nil {
		return nil, fmt.Errorf("controlplane: decode Fabric routes: %w", err)
	}
	if routes.SchemaVersion != 2 {
		return nil, fmt.Errorf("controlplane: unsupported Fabric routes schema %d", routes.SchemaVersion)
	}
	if len(routes.Routes) == 0 {
		return nil, errors.New("controlplane: Fabric route inventory is empty")
	}
	for _, route := range routes.Routes {
		if route.ID == "" || route.Xray.Address == "" || route.Xray.Port <= 0 {
			return nil, fmt.Errorf("controlplane: invalid Fabric route %q", route.ID)
		}
		if route.Xray.Protocol != "" && route.Xray.Protocol != "vless" {
			return nil, fmt.Errorf("controlplane: route %q must terminate in local VLESS", route.ID)
		}
	}
	return &XrayUserBridgeFactory{cfg: cfg, routes: routes}, nil
}

func (f *XrayUserBridgeFactory) Acquire(ctx context.Context, userID, requestID string) (UserEgressLease, error) {
	identity, err := f.cfg.IdentityResolver.ResolveFabricIdentity(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("controlplane: resolve Fabric user identity: %w", err)
	}

	dir, err := os.MkdirTemp(f.cfg.WorkDir, "fabric-xray-bridge-*")
	if err != nil {
		return nil, err
	}
	cleanupDir := true
	defer func() {
		if cleanupDir {
			_ = os.RemoveAll(dir)
		}
	}()

	xrayConfig, egressConfig, listenAddresses, err := buildUserBridgeConfigs(f.routes, identity)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(dir, "xray.json")
	egressPath := filepath.Join(dir, "egresses.json")
	if err := writeJSONFile(configPath, xrayConfig, 0600); err != nil {
		return nil, err
	}
	if err := writeJSONFile(egressPath, egressConfig, 0600); err != nil {
		return nil, err
	}

	logPath := filepath.Join(dir, "xray.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(f.cfg.XrayBinary, "run", "-config", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("controlplane: start user Xray bridge: %w", err)
	}
	_ = logFile.Close()

	lease := &xrayUserBridgeLease{
		cmd:        cmd,
		configPath: configPath,
		egressPath: egressPath,
		dir:        dir,
	}
	if err := waitForTCPListeners(ctx, listenAddresses, f.cfg.ReadyTimeout, cmd); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("controlplane: user Xray bridge did not become ready: %w (log: %s)", err, logPath)
	}
	cleanupDir = false
	return lease, nil
}

func (l *xrayUserBridgeLease) EgressConfigPath() string {
	if l == nil {
		return ""
	}
	return l.egressPath
}

func (l *xrayUserBridgeLease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.cmd != nil && l.cmd.Process != nil && l.cmd.ProcessState == nil {
			l.closeErr = l.cmd.Process.Kill()
			_, _ = l.cmd.Process.Wait()
		}
		if err := os.RemoveAll(l.dir); l.closeErr == nil && err != nil {
			l.closeErr = err
		}
	})
	return l.closeErr
}

func buildUserBridgeConfigs(routes FabricRouteInventory, identity FabricUserIdentity) (map[string]any, map[string]any, []string, error) {
	if strings.TrimSpace(identity.VLESSUUID) == "" {
		return nil, nil, nil, errors.New("controlplane: empty VLESS UUID")
	}

	inbounds := make([]any, 0, len(routes.Routes))
	outbounds := make([]any, 0, len(routes.Routes)+1)
	rules := make([]any, 0, len(routes.Routes))
	egresses := make([]any, 0, len(routes.Routes))
	listeners := make([]string, 0, len(routes.Routes))

	for index, route := range routes.Routes {
		port, err := reserveLocalPort()
		if err != nil {
			return nil, nil, nil, err
		}
		inTag := fmt.Sprintf("FABRIC-USER-SOCKS-%d", index)
		outTag := fmt.Sprintf("FABRIC-USER-VLESS-%d", index)
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		listeners = append(listeners, address)

		inbounds = append(inbounds, map[string]any{
			"tag":      inTag,
			"listen":   "127.0.0.1",
			"port":     port,
			"protocol": "socks",
			"settings": map[string]any{
				"auth": "noauth",
				"udp":  true,
				"ip":   "127.0.0.1",
			},
		})
		outbounds = append(outbounds, map[string]any{
			"tag":      outTag,
			"protocol": "vless",
			"settings": map[string]any{
				"vnext": []any{
					map[string]any{
						"address": route.Xray.Address,
						"port":    route.Xray.Port,
						"users": []any{
							map[string]any{
								"id":         identity.VLESSUUID,
								"level":      0,
								"encryption": "none",
							},
						},
					},
				},
			},
			"streamSettings": map[string]any{
				"network":  "raw",
				"security": "none",
				"rawSettings": map[string]any{
					"header": map[string]any{"type": "none"},
				},
			},
		})
		rules = append(rules, map[string]any{
			"inboundTag":  []string{inTag},
			"outboundTag": outTag,
		})
		egresses = append(egresses, map[string]any{
			"id":          route.ID,
			"type":        "socks5",
			"address":     address,
			"enabled":     true,
			"displayName": route.DisplayName,
			"region":      route.Region,
			"mode":        route.Mode,
			"auto":        route.Auto,
			"default":     route.Default,
		})
	}

	xrayConfig := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules":          rules,
		},
	}
	egressConfig := map[string]any{
		"schemaVersion":   1,
		"defaultEgress":   routes.DefaultRoute,
		"probeAddress":    "1.1.1.1:443",
		"egresses":        egresses,
		"fabricUserAware": true,
	}
	return xrayConfig, egressConfig, listeners, nil
}

func reserveLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port, nil
}

func writeJSONFile(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, mode)
}

func waitForTCPListeners(ctx context.Context, addresses []string, timeout time.Duration, cmd *exec.Cmd) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	remaining := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		remaining[address] = struct{}{}
	}
	for len(remaining) > 0 {
		if cmd != nil && cmd.ProcessState != nil {
			return errors.New("xray process exited")
		}
		for address := range remaining {
			conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				delete(remaining, address)
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timeout waiting for %d listeners", len(remaining))
		case <-ticker.C:
		}
	}
	return nil
}
