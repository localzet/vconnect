package joiner

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func parseSecureURL(rawURL, requiredScheme, label string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid %s", label)
	}
	if parsed.Scheme != requiredScheme || parsed.Hostname() == "" || parsed.User != nil {
		return nil, fmt.Errorf("%s must use %s with a hostname", label, requiredScheme)
	}
	return parsed, nil
}

func checkSecureRedirect(request *http.Request, via []*http.Request) error {
	if request.URL.Scheme != "https" || request.URL.Hostname() == "" || request.URL.User != nil {
		return http.ErrUseLastResponse
	}
	if len(via) == 0 {
		return http.ErrUseLastResponse
	}
	previousURL := via[len(via)-1].URL
	if previousURL.Scheme != "https" || !sameHTTPSOrigin(previousURL, request.URL) {
		return http.ErrUseLastResponse
	}
	return nil
}

func sameHTTPSOrigin(left, right *url.URL) bool {
	leftPort := left.Port()
	if leftPort == "" {
		leftPort = "443"
	}
	rightPort := right.Port()
	if rightPort == "" {
		rightPort = "443"
	}
	return strings.EqualFold(left.Hostname(), right.Hostname()) && leftPort == rightPort
}
