package main

import "testing"

func TestAppVersionFromPage(t *testing.T) {
	tests := []struct {
		name string
		page string
		want string
	}{
		{
			name: "legacy nested config",
			page: `<script id="preloaded-state">{"config":{"appVersion":"1.0.1"}}</script>`,
			want: "1.0.1",
		},
		{
			name: "legacy top-level config",
			page: `<script id="preloaded-state">{"appVersion":"210.5.0"}</script>`,
			want: "210.5.0",
		},
		{
			name: "modern inline config",
			page: `<script type="application/json" id="fedya-config">{"apiVersion":5}</script><script>window.state={appVersion: "1.0.1"}</script>`,
			want: "1.0.1",
		},
		{
			name: "missing version",
			page: `<script id="fedya-config">{"apiVersion":5}</script>`,
			want: "",
		},
		{
			name: "rejects malformed version",
			page: `<script>window.state={appVersion:"latest"}</script>`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appVersionFromPage([]byte(tt.page)); got != tt.want {
				t.Fatalf("appVersionFromPage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSDKVersionFromBundle(t *testing.T) {
	tests := []struct {
		name   string
		bundle string
		want   string
	}{
		{
			name:   "legacy variable",
			bundle: `goloom_sdk_version:"6.5.0"`,
			want:   "6.5.0",
		},
		{
			name:   "package metadata",
			bundle: `"@yandex-video-platform/goloom-sdk":"6.6.1"`,
			want:   "6.6.1",
		},
		{
			name:   "asset path",
			bundle: `goloom-sdk.6.7.2.js`,
			want:   "6.7.2",
		},
		{
			name:   "no marker",
			bundle: `window.enableGoloom=true`,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sdkVersionFromBundle([]byte(tt.bundle)); got != tt.want {
				t.Fatalf("sdkVersionFromBundle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSDKVersionFallback(t *testing.T) {
	got, usedFallback := sdkVersionOrFallback(nil)
	if got != fallbackSDKVersion {
		t.Fatalf("sdkVersionOrFallback() = %q, want %q", got, fallbackSDKVersion)
	}
	if !usedFallback {
		t.Fatal("sdkVersionOrFallback() did not report the fallback")
	}
	got, usedFallback = sdkVersionOrFallback([]byte(`goloom_sdk_version:"6.6.1"`))
	if got != "6.6.1" {
		t.Fatalf("sdkVersionOrFallback() = %q, want %q", got, "6.6.1")
	}
	if usedFallback {
		t.Fatal("sdkVersionOrFallback() reported fallback for a parsed version")
	}
}
