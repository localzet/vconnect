package controlplane

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	PlatformVK       = "vk"
	PlatformTelemost = "telemost"
	PlatformWBStream = "wbstream"
	PlatformDION     = "dion"
)

type CookieResolver interface {
	CookiePath(userID, platform string) (string, error)
}

type StaticCookieResolver map[string]string

func (r StaticCookieResolver) CookiePath(_ string, platform string) (string, error) {
	path := strings.TrimSpace(r[platform])
	if path == "" && platform == PlatformTelemost {
		path = strings.TrimSpace(r["tm"])
	}
	if path == "" {
		return "", fmt.Errorf("controlplane: cookies for platform %q are not configured", platform)
	}
	return path, nil
}

type ProcessFactoryConfig struct {
	BinsDir         string
	SessionsDir     string
	Resources       string
	EgressConfig    string
	UserEgresses    UserEgressFactory
	Cookies         CookieResolver
	ServiceCookies  CookieResolver
	CookieSource    string
	SpawnTimeout    time.Duration
	DefaultPlatform string
	VKPeerID        string
}

type managedWorkProcess struct {
	cmd   *exec.Cmd
	lease UserEgressLease
}

type ProcessWorkCallFactory struct {
	cfg       ProcessFactoryConfig
	mu        sync.Mutex
	processes map[string]*managedWorkProcess
}

func NewProcessWorkCallFactory(cfg ProcessFactoryConfig) (*ProcessWorkCallFactory, error) {
	cfg.BinsDir = strings.TrimSpace(cfg.BinsDir)
	if cfg.BinsDir == "" {
		return nil, errors.New("controlplane: bins dir is required")
	}
	if cfg.SessionsDir == "" {
		cfg.SessionsDir = os.TempDir()
	}
	if cfg.Resources == "" {
		cfg.Resources = "default"
	}
	if cfg.SpawnTimeout <= 0 {
		cfg.SpawnTimeout = 60 * time.Second
	}
	if cfg.DefaultPlatform == "" {
		cfg.DefaultPlatform = PlatformTelemost
	}
	cfg.CookieSource = normalizeCookieSource(cfg.CookieSource)
	if cfg.CookieSource != "user" && cfg.CookieSource != "service" {
		return nil, fmt.Errorf("controlplane: unsupported cookie source %q", cfg.CookieSource)
	}
	return &ProcessWorkCallFactory{
		cfg:       cfg,
		processes: make(map[string]*managedWorkProcess),
	}, nil
}

func (f *ProcessWorkCallFactory) CreateWorkCall(ctx context.Context, request WorkCallRequest) (WorkCall, error) {
	platform := normalizePlatform(request.Platform)
	if platform == "" {
		platform = normalizePlatform(f.cfg.DefaultPlatform)
	}
	binName, err := creatorBinary(platform)
	if err != nil {
		return WorkCall{}, err
	}
	bin, err := resolveBinary(f.cfg.BinsDir, binName)
	if err != nil {
		return WorkCall{}, err
	}
	if err := os.MkdirAll(f.cfg.SessionsDir, 0755); err != nil {
		return WorkCall{}, err
	}
	linkFile, err := os.CreateTemp(f.cfg.SessionsDir, "work-link-*.txt")
	if err != nil {
		return WorkCall{}, err
	}
	linkPath := linkFile.Name()
	linkFile.Close()
	os.Remove(linkPath)

	egressConfigPath := f.cfg.EgressConfig
	var egressLease UserEgressLease
	if f.cfg.UserEgresses != nil {
		egressLease, err = f.cfg.UserEgresses.Acquire(ctx, request.UserID, request.RequestID)
		if err != nil {
			return WorkCall{}, err
		}
		egressConfigPath = egressLease.EgressConfigPath()
	}
	cleanupLease := func() {
		if egressLease != nil {
			_ = egressLease.Close()
		}
	}

	args := []string{"--write-file", linkPath, "--resources", f.cfg.Resources}
	if platform == PlatformVK {
		peerID := strings.TrimSpace(f.cfg.VKPeerID)
		if peerID == "" {
			cleanupLease()
			return WorkCall{}, errors.New("controlplane: VK work call requires configured peer id")
		}
		args = append(args, "--peer-id", peerID)
	}
	if platformNeedsCookies(platform) {
		resolver := f.cookieResolver()
		if resolver == nil {
			cleanupLease()
			return WorkCall{}, fmt.Errorf("controlplane: cookie resolver is required for platform %q", platform)
		}
		cookies, err := resolver.CookiePath(request.UserID, platform)
		if err != nil {
			cleanupLease()
			return WorkCall{}, err
		}
		args = append(args, "--cookies", cookies)
	}
	if egressConfigPath != "" {
		args = append(args, "--egress-config", egressConfigPath)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	logPath := filepath.Join(f.cfg.SessionsDir, fmt.Sprintf("work-%s-%s.log", platform, request.RequestID))
	logFile, err := os.Create(logPath)
	if err != nil {
		cleanupLease()
		return WorkCall{}, err
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		cleanupLease()
		return WorkCall{}, err
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	link, err := waitForFirstLine(ctx, linkPath, f.cfg.SpawnTimeout, waitCh)
	if err != nil {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-waitCh
		}
		cleanupLease()
		return WorkCall{}, fmt.Errorf("%w (log: %s)", err, logPath)
	}
	if cmd.ProcessState != nil {
		cleanupLease()
		return WorkCall{}, fmt.Errorf("controlplane: creator exited after writing link (log: %s)", logPath)
	}
	select {
	case err := <-waitCh:
		if err != nil {
			cleanupLease()
			return WorkCall{}, fmt.Errorf("controlplane: creator exited after writing link: %w (log: %s)", err, logPath)
		}
		cleanupLease()
		return WorkCall{}, fmt.Errorf("controlplane: creator exited after writing link (log: %s)", logPath)
	default:
	}
	f.mu.Lock()
	f.processes[link] = &managedWorkProcess{cmd: cmd, lease: egressLease}
	f.mu.Unlock()
	go func() {
		_ = <-waitCh
		// Если work creator завершился сам, больше держать персональный
		// Xray bridge нет смысла. Close идемпотентен, поэтому normal cleanup
		// может вызвать его повторно.
		cleanupLease()
	}()
	return WorkCall{JoinLink: link}, nil
}

func (f *ProcessWorkCallFactory) cookieResolver() CookieResolver {
	if f.cfg.CookieSource == "service" && f.cfg.ServiceCookies != nil {
		return f.cfg.ServiceCookies
	}
	return f.cfg.Cookies
}

func (f *ProcessWorkCallFactory) CloseWorkCall(_ context.Context, session Session) error {
	f.mu.Lock()
	managed := f.processes[session.JoinLink]
	delete(f.processes, session.JoinLink)
	f.mu.Unlock()
	if managed == nil {
		return nil
	}
	var firstErr error
	if managed.cmd != nil && managed.cmd.Process != nil && managed.cmd.ProcessState == nil {
		if err := managed.cmd.Process.Kill(); err != nil {
			firstErr = err
		}
	}
	if managed.lease != nil {
		if err := managed.lease.Close(); firstErr == nil && err != nil {
			firstErr = err
		}
	}
	return firstErr
}

func normalizeCookieSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", "user", "vault":
		return "user"
	case "service", "server":
		return "service"
	default:
		return strings.ToLower(strings.TrimSpace(source))
	}
}

func normalizePlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "tm", "telemost", "yandex":
		return PlatformTelemost
	case "vk":
		return PlatformVK
	case "wb", "wbstream":
		return PlatformWBStream
	case "dion":
		return PlatformDION
	default:
		return strings.ToLower(strings.TrimSpace(platform))
	}
}

func creatorBinary(platform string) (string, error) {
	switch platform {
	case PlatformVK:
		return "headless-vk-creator", nil
	case PlatformTelemost:
		return "headless-telemost-creator", nil
	case PlatformWBStream:
		return "headless-wbstream-creator", nil
	case PlatformDION:
		return "headless-dion-creator", nil
	default:
		return "", fmt.Errorf("controlplane: unsupported platform %q", platform)
	}
}

func platformNeedsCookies(platform string) bool {
	switch platform {
	case PlatformWBStream:
		return false
	default:
		return true
	}
}

func resolveBinary(dir, name string) (string, error) {
	exact := filepath.Join(dir, name)
	if info, err := os.Stat(exact); err == nil && !info.IsDir() {
		return exact, nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, name+"*"))
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && !info.IsDir() {
			return match, nil
		}
	}
	return "", fmt.Errorf("controlplane: binary %s not found in %s", name, dir)
}

func waitForFirstLine(ctx context.Context, path string, timeout time.Duration, processDone <-chan error) (string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-processDone:
			if err != nil {
				return "", fmt.Errorf("controlplane: creator exited before writing link: %w", err)
			}
			return "", errors.New("controlplane: creator exited before writing link")
		case <-deadline.C:
			return "", fmt.Errorf("controlplane: creator did not write link within %s", timeout)
		case <-ticker.C:
			line, ok := readFirstLine(path)
			if ok {
				return line, nil
			}
		}
	}
}

func readFirstLine(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return "", false
	}
	line := strings.TrimSpace(scanner.Text())
	return line, line != ""
}
