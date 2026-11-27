package dns

import (
	"context"
	"crypto/tls"
	"net"
)

// tlsHandshake wraps an established TCP connection in a TLS client
// handshake against the given server name.
func tlsHandshake(ctx context.Context, raw net.Conn, serverName string) (net.Conn, error) {
	cfg := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	conn := tls.Client(raw, cfg)
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}
