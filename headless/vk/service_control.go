package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"vconnect/relay/controlplane"
	"vconnect/relay/egress"
	"vconnect/relay/tunnel"
)

type serviceControlOptions struct {
	UserIDs           string
	AuthMode          string
	RemnawaveURL      string
	RemnawaveAPIToken string
	RemnawaveLookup   string
	FabricAuthKey     string
	AuthCacheTTL      time.Duration
	AuthStaleGrace    time.Duration
	VaultDir          string
	VaultKey          string
	BinsDir           string
	SessionsDir       string
	EgressConfig      string
	FabricRoutes      string
	XrayBinary        string
	Resources         string
	WorkPlatform      string
	WorkVKPeerID      string
	WorkCookieSource  string
	ServiceCookies    string
	MaxActiveUsers    int
	WorkTTL           time.Duration
	StatsFile         string
}

func configureServiceControl(opts serviceControlOptions) (func(*tunnel.RelayBridge) error, error) {
	allowedUsers := parseServiceUserIDs(opts.UserIDs)
	authMode := strings.TrimSpace(opts.AuthMode)
	if authMode == "" {
		authMode = "allowlist"
	}
	var authorizer controlplane.UserAuthorizer
	var remnawaveDirectory *controlplane.RemnawaveDirectory
	switch authMode {
	case "allowlist":
		if len(allowedUsers) == 0 {
			return nil, fmt.Errorf("--service-user-ids is required in allowlist auth mode")
		}
	case "remnawave":
		key, err := controlplane.FabricCredentialKeyFromBase64(opts.FabricAuthKey)
		if err != nil {
			return nil, fmt.Errorf("fabric auth key: %w", err)
		}
		remnawaveDirectory, err = controlplane.NewRemnawaveDirectory(controlplane.RemnawaveDirectoryConfig{
			BaseURL: opts.RemnawaveURL, APIToken: opts.RemnawaveAPIToken, LookupMode: opts.RemnawaveLookup,
		})
		if err != nil {
			return nil, err
		}
		authorizer, err = controlplane.NewRemnawaveAuthorizer(controlplane.RemnawaveAuthorizerConfig{
			BaseURL:       opts.RemnawaveURL,
			APIToken:      opts.RemnawaveAPIToken,
			LookupMode:    opts.RemnawaveLookup,
			CredentialKey: key,
			CacheTTL:      opts.AuthCacheTTL,
			StaleGrace:    opts.AuthStaleGrace,
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported service auth mode %q", authMode)
	}
	if opts.VaultDir == "" || opts.VaultKey == "" || opts.BinsDir == "" {
		return nil, fmt.Errorf("--vault-dir, --vault-key-base64 and --bins-dir are required")
	}
	if opts.MaxActiveUsers <= 0 || opts.WorkTTL <= 0 {
		return nil, fmt.Errorf("--max-active-users and --work-ttl must be positive")
	}

	key, err := controlplane.CookieVaultKeyFromBase64(opts.VaultKey)
	if err != nil {
		return nil, fmt.Errorf("vault key: %w", err)
	}
	vault, err := controlplane.NewCookieVault(opts.VaultDir, key)
	if err != nil {
		return nil, fmt.Errorf("cookie vault: %w", err)
	}
	registry := egress.DirectRegistry()
	var userEgresses controlplane.UserEgressFactory
	if opts.FabricRoutes != "" {
		if remnawaveDirectory == nil {
			return nil, fmt.Errorf("--fabric-routes requires remnawave auth mode")
		}
		registry, err = controlplane.LoadFabricRouteCatalog(opts.FabricRoutes)
		if err != nil {
			return nil, fmt.Errorf("Fabric route catalog: %w", err)
		}
		userEgresses, err = controlplane.NewXrayUserBridgeFactory(controlplane.XrayUserBridgeConfig{
			XrayBinary: opts.XrayBinary, RoutesPath: opts.FabricRoutes, WorkDir: opts.SessionsDir,
			IdentityResolver: controlplane.RemnawaveIdentityResolver{Directory: remnawaveDirectory},
		})
		if err != nil {
			return nil, fmt.Errorf("Fabric user bridge: %w", err)
		}
	} else if opts.EgressConfig != "" {
		registry, err = egress.LoadConfig(opts.EgressConfig)
		if err != nil {
			return nil, fmt.Errorf("egress config: %w", err)
		}
	}
	factory, err := controlplane.NewProcessWorkCallFactory(controlplane.ProcessFactoryConfig{
		BinsDir:         opts.BinsDir,
		SessionsDir:     opts.SessionsDir,
		Resources:       opts.Resources,
		EgressConfig:    opts.EgressConfig,
		UserEgresses:    userEgresses,
		Cookies:         vault,
		ServiceCookies:  controlplane.StaticCookieResolver{controlplane.PlatformVK: opts.ServiceCookies},
		CookieSource:    opts.WorkCookieSource,
		DefaultPlatform: opts.WorkPlatform,
		VKPeerID:        opts.WorkVKPeerID,
	})
	if err != nil {
		return nil, fmt.Errorf("work factory: %w", err)
	}
	manager := controlplane.NewManager(controlplane.Config{MaxUsers: opts.MaxActiveUsers, WorkTTL: opts.WorkTTL})
	if strings.TrimSpace(opts.StatsFile) != "" {
		go writeServiceStats(opts.StatsFile, manager)
	}
	orchestrator, err := controlplane.NewOrchestrator(manager, registry, factory, opts.WorkTTL)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: %w", err)
	}
	go cleanupExpiredWorkCalls(orchestrator)

	handler := controlplane.ServiceHandler{
		AllowedUserIDs: allowedUsers,
		Authorizer:     authorizer,
		CookieVault:    vault,
		Sessions:       orchestrator,
	}
	log.Printf("[service] transport=vk auth=%s allowed-users=%d max-active-users=%d work-ttl=%s work-cookie-source=%s", authMode, len(allowedUsers), opts.MaxActiveUsers, opts.WorkTTL, opts.WorkCookieSource)
	return func(bridge *tunnel.RelayBridge) error {
		bridge.SetEgressRegistry(registry)
		return handler.BindBridge(context.Background(), bridge)
	}, nil
}

func parseServiceUserIDs(csv string) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, value := range strings.Split(csv, ",") {
		if userID := strings.TrimSpace(value); userID != "" {
			allowed[userID] = struct{}{}
		}
	}
	return allowed
}

func cleanupExpiredWorkCalls(orchestrator *controlplane.Orchestrator) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if err := orchestrator.CleanupExpired(context.Background()); err != nil {
			log.Printf("[service] cleanup expired work calls: %v", err)
		}
	}
}

func writeServiceStats(path string, manager *controlplane.Manager) {
	path = strings.TrimSpace(path)
	if path == "" || manager == nil {
		return
	}
	write := func() {
		temporary := path + ".tmp"
		if err := os.WriteFile(temporary, []byte(fmt.Sprintf("%d\n", manager.ActiveUserCount())), 0644); err != nil {
			log.Printf("[service] write stats: %v", err)
			return
		}
		if err := os.Rename(temporary, path); err != nil {
			log.Printf("[service] publish stats: %v", err)
		}
	}
	write()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		write()
	}
}
