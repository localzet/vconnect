package egress

import (
	"fmt"
	"net"
	"time"

	"vconnect/relay/common"
)

type DirectDialer struct {
	ProfileID string
}

func (d DirectDialer) ID() string {
	if d.ProfileID == "" {
		return TypeDirect
	}
	return d.ProfileID
}

func (d DirectDialer) DialTCP(dst string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", dst, timeout)
}

func (d DirectDialer) UDPAssociate(dst string, timeout time.Duration) (UDPSession, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", dst)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, err
	}
	return &directUDPSession{conn: conn}, nil
}

type SOCKS5Dialer struct {
	ProfileID string
	Upstream  *common.Socks5Upstream
}

func (d SOCKS5Dialer) ID() string {
	return d.ProfileID
}

func (d SOCKS5Dialer) DialTCP(dst string, timeout time.Duration) (net.Conn, error) {
	return d.Upstream.DialTCP(dst, timeout)
}

func (d SOCKS5Dialer) UDPAssociate(_ string, timeout time.Duration) (UDPSession, error) {
	return d.Upstream.UDPAssociate(timeout)
}

type directUDPSession struct {
	conn *net.UDPConn
}

func (s *directUDPSession) WriteTo(data []byte, _ string) error {
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := s.conn.Write(data)
	return err
}

func (s *directUDPSession) Read(buf []byte) (int, error) {
	return s.conn.Read(buf)
}

func (s *directUDPSession) SetReadDeadline(t time.Time) error {
	return s.conn.SetReadDeadline(t)
}

func (s *directUDPSession) Close() error {
	return s.conn.Close()
}

// ReachabilityDialer используется control plane только для проверки того,
// что локальная точка Fabric/Xray вообще слушает порт. Пользовательский трафик
// через него не отправляется: реальный egress создаётся персональным bridge.
type ReachabilityDialer struct {
	ProfileID string
	Target    string
}

func (d ReachabilityDialer) ID() string {
	return d.ProfileID
}

func (d ReachabilityDialer) DialTCP(_ string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", d.Target, timeout)
}

func (d ReachabilityDialer) UDPAssociate(_ string, _ time.Duration) (UDPSession, error) {
	return nil, fmt.Errorf("egress: reachability dialer does not support UDP")
}
