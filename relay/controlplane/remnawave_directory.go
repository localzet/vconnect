package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RemnawaveUser содержит только те поля, которые нужны Fabric на RU edge.
// Полная пользовательская модель остаётся собственностью Remnawave.
type RemnawaveUser struct {
	ID        string
	ShortUUID string
	Username  string
	VLESSUUID string
	Status    string
}

type RemnawaveDirectoryConfig struct {
	BaseURL    string
	APIToken   string
	LookupMode string
	HTTPClient *http.Client
}

type RemnawaveDirectory struct {
	baseURL    string
	apiToken   string
	lookupMode string
	httpClient *http.Client
}

func NewRemnawaveDirectory(config RemnawaveDirectoryConfig) (*RemnawaveDirectory, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("controlplane: remnawave base URL is required")
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("controlplane: invalid remnawave base URL: %w", err)
	}
	if strings.TrimSpace(config.APIToken) == "" {
		return nil, errors.New("controlplane: remnawave API token is required")
	}

	lookupMode := strings.TrimSpace(config.LookupMode)
	if lookupMode == "" {
		lookupMode = RemnawaveLookupShortUUID
	}
	switch lookupMode {
	case RemnawaveLookupShortUUID, RemnawaveLookupUsername, RemnawaveLookupID:
	default:
		return nil, fmt.Errorf("controlplane: unsupported remnawave lookup mode %q", lookupMode)
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	return &RemnawaveDirectory{
		baseURL:    baseURL,
		apiToken:   strings.TrimSpace(config.APIToken),
		lookupMode: lookupMode,
		httpClient: httpClient,
	}, nil
}

func (d *RemnawaveDirectory) Lookup(ctx context.Context, userID string) (RemnawaveUser, error) {
	userID = normalizeUserID(userID)
	if userID == "" {
		return RemnawaveUser{}, errors.New("controlplane: user id is required")
	}
	endpoint, err := d.userEndpoint(userID)
	if err != nil {
		return RemnawaveUser{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return RemnawaveUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+d.apiToken)
	req.Header.Set("Accept", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return RemnawaveUser{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return RemnawaveUser{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return RemnawaveUser{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return RemnawaveUser{}, fmt.Errorf("decode user response: %w", err)
	}
	user := RemnawaveUser{}
	user.ID, _ = findStringField(payload, "id")
	user.ShortUUID, _ = findStringField(payload, "shortUuid")
	user.Username, _ = findStringField(payload, "username")
	user.VLESSUUID, _ = findStringField(payload, "vlessUuid")
	user.Status, _ = findStringField(payload, "status")
	if user.Status == "" {
		return RemnawaveUser{}, errors.New("user response does not contain status")
	}
	return user, nil
}

func (d *RemnawaveDirectory) userEndpoint(userID string) (string, error) {
	escaped := url.PathEscape(userID)
	switch d.lookupMode {
	case RemnawaveLookupShortUUID:
		return d.baseURL + "/api/users/by-short-uuid/" + escaped, nil
	case RemnawaveLookupUsername:
		return d.baseURL + "/api/users/by-username/" + escaped, nil
	case RemnawaveLookupID:
		return d.baseURL + "/api/users/" + escaped, nil
	default:
		return "", fmt.Errorf("unsupported remnawave lookup mode %q", d.lookupMode)
	}
}

type FabricUserIdentity struct {
	UserID    string
	VLESSUUID string
}

type FabricUserIdentityResolver interface {
	ResolveFabricIdentity(ctx context.Context, userID string) (FabricUserIdentity, error)
}

type RemnawaveIdentityResolver struct {
	Directory *RemnawaveDirectory
}

func (r RemnawaveIdentityResolver) ResolveFabricIdentity(ctx context.Context, userID string) (FabricUserIdentity, error) {
	if r.Directory == nil {
		return FabricUserIdentity{}, errors.New("controlplane: remnawave directory is required")
	}
	user, err := r.Directory.Lookup(ctx, userID)
	if err != nil {
		return FabricUserIdentity{}, err
	}
	if !strings.EqualFold(user.Status, "ACTIVE") {
		return FabricUserIdentity{}, errors.New("controlplane: remnawave user is not active")
	}
	if strings.TrimSpace(user.VLESSUUID) == "" {
		return FabricUserIdentity{}, errors.New("controlplane: remnawave user has no vlessUuid")
	}
	canonical := normalizeUserID(user.ShortUUID)
	if canonical == "" {
		canonical = normalizeUserID(userID)
	}
	return FabricUserIdentity{UserID: canonical, VLESSUUID: user.VLESSUUID}, nil
}
