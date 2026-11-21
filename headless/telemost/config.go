package main

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"vconnect/relay/common"
)

type TMConfig struct {
	AppVersion string
	SDKVersion string
}

const fallbackSDKVersion = "6.5.0"

var (
	preloadedStatePattern = regexp.MustCompile(`<script[^>]*id="preloaded-state"[^>]*>([\s\S]*?)</script>`)
	appVersionPattern     = regexp.MustCompile(`(?i)["']?appVersion["']?\s*:\s*["']([^"'\\]+)["']`)
	validVersionPattern   = regexp.MustCompile(`^\d+(?:\.\d+){1,3}(?:[-+][A-Za-z0-9.-]+)?$`)
	bundleURLPattern      = regexp.MustCompile(`https://telemost\.yastatic\.net/s3/telemost/_/main\.\w+\.[a-f0-9]+\.js`)
	sdkVersionPatterns    = []*regexp.Regexp{
		regexp.MustCompile(`goloom_sdk_version\s*:\s*["'](\d+\.\d+\.\d+)["']`),
		regexp.MustCompile(`"@yandex-video-platform/goloom-sdk"\s*:\s*"(\d+\.\d+\.\d+)"`),
		regexp.MustCompile(`goloom-sdk\.(\d+\.\d+\.\d+)\.js`),
	}
)

func fetchConfig() (TMConfig, error) {
	var cfg TMConfig

	page, err := common.HttpGet("https://telemost.yandex.ru/")
	if err != nil {
		return cfg, fmt.Errorf("failed to fetch telemost.yandex.ru: %w", err)
	}

	cfg.AppVersion = appVersionFromPage(page)
	if cfg.AppVersion == "" {
		return cfg, fmt.Errorf("appVersion not found in page")
	}
	log.Printf("[config] appVersion=%s", cfg.AppVersion)

	var bundle []byte
	if bundleURL := bundleURLPattern.FindString(string(page)); bundleURL != "" {
		log.Printf("[config] Found bundle: %s", bundleURL)
		bundle, err = common.HttpGet(bundleURL)
		if err != nil {
			log.Printf("[config] Unable to fetch SDK version bundle: %v", err)
			bundle = nil
		}
	}
	var usedFallback bool
	cfg.SDKVersion, usedFallback = sdkVersionOrFallback(bundle)
	if usedFallback {
		log.Printf("[config] SDK version marker unavailable; using last verified version %s", cfg.SDKVersion)
	}

	log.Printf("[config] app=%s sdk=%s", cfg.AppVersion, cfg.SDKVersion)
	return cfg, nil
}

func appVersionFromPage(page []byte) string {
	if stateMatch := preloadedStatePattern.FindSubmatch(page); stateMatch != nil {
		var state struct {
			Config struct {
				AppVersion string `json:"appVersion"`
			} `json:"config"`
			AppVersion string `json:"appVersion"`
		}
		if err := json.Unmarshal(stateMatch[1], &state); err == nil {
			if version := validAppVersion(state.Config.AppVersion); version != "" {
				return version
			}
			if version := validAppVersion(state.AppVersion); version != "" {
				return version
			}
		}
	}

	for _, match := range appVersionPattern.FindAllSubmatch(page, -1) {
		if version := validAppVersion(string(match[1])); version != "" {
			return version
		}
	}
	return ""
}

func validAppVersion(version string) string {
	version = strings.TrimSpace(version)
	if !validVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

func sdkVersionFromBundle(bundle []byte) string {
	for _, pattern := range sdkVersionPatterns {
		if match := pattern.FindSubmatch(bundle); match != nil {
			return string(match[1])
		}
	}
	return ""
}

func sdkVersionOrFallback(bundle []byte) (string, bool) {
	if version := sdkVersionFromBundle(bundle); version != "" {
		return version, false
	}
	return fallbackSDKVersion, true
}
