package joiner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

func verifiedTLSConfig(serverName string, roots *x509.CertPool) *tls.Config {
	return &tls.Config{ServerName: serverName, RootCAs: roots}
}

func newResolvedWebSocketDialer(resolvedIP, serverName string, roots *x509.CertPool) websocket.Dialer {
	return websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		WriteBufferSize:  65536,
		TLSClientConfig:  verifiedTLSConfig(serverName, roots),
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			target := net.JoinHostPort(resolvedIP, port)
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, target)
		},
	}
}
