package joiner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

type testStatusEmitter struct {
	statusError string
}

func (emitter *testStatusEmitter) EmitStatus(string) {}

func (emitter *testStatusEmitter) EmitStatusError(message string) {
	emitter.statusError = message
}

func TestTelemostHTTPClientRejectsUntrustedCertificate(t *testing.T) {
	t.Setenv("NO_PROXY", "example.com")
	t.Setenv("no_proxy", "example.com")
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	requestURL := "https://example.com:" + serverURL.Port()

	resolvedHost := make(chan string, 1)
	joiner := NewTelemostHeadlessJoiner(func(string, ...any) {}, func(host string) (string, error) {
		select {
		case resolvedHost <- host:
		default:
		}
		if host != "example.com" {
			return "", fmt.Errorf("unexpected host %q", host)
		}
		return "127.0.0.1", nil
	}, nil, nil, nil, nil)
	response, err := joiner.makeHTTPClient().Get(requestURL)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("HTTP client accepted an untrusted certificate")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("request error = %v, want certificate verification error", err)
	}
	select {
	case host := <-resolvedHost:
		if host != "example.com" {
			t.Fatalf("resolved host = %q, want example.com", host)
		}
	default:
		t.Fatal("request did not use the configured hostname resolver")
	}
}

func TestResolvedWebSocketDialerRejectsUntrustedCertificate(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader.Upgrade(writer, request, nil)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	webSocketURL := "wss://example.com:" + serverURL.Port()
	dialer := newResolvedWebSocketDialer(net.ParseIP("127.0.0.1").String(), "example.com", nil)
	connection, response, err := dialer.Dial(webSocketURL, nil)
	if connection != nil {
		connection.Close()
	}
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("WebSocket dialer accepted an untrusted certificate")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("dial error = %v, want certificate verification error", err)
	}
}

func TestResolvedWebSocketDialerAcceptsTrustedCertificate(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader.Upgrade(writer, request, nil)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dialer := newResolvedWebSocketDialer("127.0.0.1", "example.com", roots)
	connection, response, err := dialer.Dial("wss://example.com:"+serverURL.Port(), nil)
	if response != nil {
		response.Body.Close()
	}
	if err != nil {
		t.Fatalf("trusted WebSocket dial failed: %v", err)
	}
	connection.Close()
}

func TestResolvedWebSocketDialerRejectsHostnameMismatch(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader.Upgrade(writer, request, nil)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dialer := newResolvedWebSocketDialer("127.0.0.1", "attacker.other.test", roots)
	connection, response, err := dialer.Dial("wss://example.com:"+serverURL.Port(), nil)
	if connection != nil {
		connection.Close()
	}
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("dial error = %v, want hostname verification error", err)
	}
}

func TestSecureURLRejectsProtocolDowngrade(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		scheme string
	}{
		{name: "API over HTTP", url: "http://api.example.test/path", scheme: "https"},
		{name: "media over WS", url: "ws://media.example.test/path", scheme: "wss"},
		{name: "missing hostname", url: "https:///path", scheme: "https"},
		{name: "userinfo", url: "https://user@api.example.test/path", scheme: "https"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseSecureURL(test.url, test.scheme, "endpoint"); err == nil {
				t.Fatal("insecure endpoint was accepted")
			}
		})
	}
	for _, endpoint := range []struct {
		url    string
		scheme string
	}{{url: "https://api.example.test/path", scheme: "https"}, {url: "wss://media.example.test/path", scheme: "wss"}} {
		if _, err := parseSecureURL(endpoint.url, endpoint.scheme, "endpoint"); err != nil {
			t.Fatalf("valid %s endpoint rejected: %v", endpoint.scheme, err)
		}
	}
}

func TestHTTPSClientRejectsRedirectDowngrade(t *testing.T) {
	plaintextRequests := make(chan struct{}, 1)
	plaintextServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		select {
		case plaintextRequests <- struct{}{}:
		default:
		}
	}))
	defer plaintextServer.Close()

	secureServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, plaintextServer.URL, http.StatusTemporaryRedirect)
	}))
	defer secureServer.Close()

	client := secureServer.Client()
	client.CheckRedirect = checkSecureRedirect
	response, err := client.Get(secureServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("response status = %d, want redirect response", response.StatusCode)
	}
	select {
	case <-plaintextRequests:
		t.Fatal("client followed an HTTPS-to-HTTP redirect")
	default:
	}
}

func TestHTTPSClientAllowsSameOriginRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "/done", http.StatusTemporaryRedirect)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := server.Client()
	client.CheckRedirect = checkSecureRedirect
	response, err := client.Get(server.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func TestHTTPSClientRejectsCrossHostPOSTRedirect(t *testing.T) {
	forwardedBody := make(chan []byte, 1)
	target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err == nil {
			forwardedBody <- body
		}
	}))
	defer target.Close()

	targetAddress := target.Listener.Addr().String()
	_, targetPort, err := net.SplitHostPort(targetAddress)
	if err != nil {
		t.Fatal(err)
	}
	targetURL := "https://attacker.example.com:" + targetPort + "/capture"
	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, targetURL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	sourceURLValue, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	sourcePort := sourceURLValue.Port()
	sourceURL := "https://example.com:" + sourcePort + "/submit"
	roots := x509.NewCertPool()
	roots.AddCert(source.Certificate())
	roots.AddCert(target.Certificate())
	addresses := map[string]string{
		sourcePort: source.Listener.Addr().String(),
		targetPort: targetAddress,
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			targetAddress, ok := addresses[port]
			if !ok {
				return nil, fmt.Errorf("unexpected test port")
			}
			return (&net.Dialer{}).DialContext(ctx, network, targetAddress)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, CheckRedirect: checkSecureRedirect}
	response, err := client.Post(sourceURL, "application/x-www-form-urlencoded", strings.NewReader("session=canary"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("response status = %d, want redirect response", response.StatusCode)
	}
	select {
	case <-forwardedBody:
		t.Fatal("client forwarded a POST body to a cross-host HTTPS redirect")
	default:
	}
}

func TestTelemostRejectsPlainWebSocketEndpoint(t *testing.T) {
	status := &testStatusEmitter{}
	resolved := false
	joiner := NewTelemostHeadlessJoiner(func(string, ...any) {}, func(string) (string, error) {
		resolved = true
		return "127.0.0.1", nil
	}, status, nil, nil, nil)
	joiner.mediaURL = "ws://media.example.test/socket"
	defer joiner.Close()

	joiner.connectAndRun()
	if resolved {
		t.Fatal("Telemost resolved a host before rejecting a plaintext WebSocket URL")
	}
	if status.statusError != "invalid secure media URL" {
		t.Fatalf("status error = %q, want invalid secure media URL", status.statusError)
	}
}

func TestVKJoinCallRejectsPlainHTTPAPIBaseURL(t *testing.T) {
	resolved := false
	joiner := NewVKHeadlessJoiner(func(string, ...any) {}, func(string) (string, error) {
		resolved = true
		return "127.0.0.1", nil
	}, nil, nil, nil, nil)
	joiner.authParams = &VKHeadlessAuthParams{APIBaseURL: "http://api.example.test/fb.do"}

	if err := joiner.joinCall(); err == nil {
		t.Fatal("VK accepted a plaintext API URL")
	}
	if resolved {
		t.Fatal("VK resolved a host before rejecting a plaintext API URL")
	}
}

func TestVKConnectSFURejectsPlainWebSocketEndpoint(t *testing.T) {
	resolved := false
	joiner := NewVKHeadlessJoiner(func(string, ...any) {}, func(string) (string, error) {
		resolved = true
		return "127.0.0.1", nil
	}, nil, nil, nil, nil)
	joiner.joinResp = &VKJoinResponse{Endpoint: "ws://media.example.test/socket?token=redacted"}

	if err := joiner.connectSFU(); err == nil {
		t.Fatal("VK accepted a plaintext WebSocket URL")
	}
	if resolved {
		t.Fatal("VK resolved a host before rejecting a plaintext WebSocket URL")
	}
}
