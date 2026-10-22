// Command promlike models the use case that motivated
// https://github.com/spiffe/spiffe/issues/414: a dependency-conscious workload
// (think Prometheus) that receives its SPIFFE identity from files on disk
// rather than the Workload API. It imports only github.com/spiffe/go-spiffe/lite
// packages, so gRPC and protobuf never enter its module graph.
package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/spiffe/go-spiffe/lite/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/lite/spiffeid"
	"github.com/spiffe/go-spiffe/lite/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/lite/svid/x509svid"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	td, err := spiffeid.TrustDomainFromString("example.org")
	if err != nil {
		return err
	}
	serverID, err := spiffeid.FromPath(td, "/server")
	if err != nil {
		return err
	}
	clientID, err := spiffeid.FromPath(td, "/client")
	if err != nil {
		return err
	}

	// Stand in for the delivery mechanism: some agent (spiffe-helper, an
	// init container, the upcoming filesystem delivery implementation)
	// placed SVIDs and the trust bundle on disk before the workload starts.
	deliveryDir, err := os.MkdirTemp("", "svid-delivery")
	if err != nil {
		return err
	}
	defer os.RemoveAll(deliveryDir)
	if err := writeDeliveredIdentities(deliveryDir, serverID, clientID); err != nil {
		return err
	}

	serverSVID, err := x509svid.Load(
		filepath.Join(deliveryDir, "server_svid.pem"),
		filepath.Join(deliveryDir, "server_key.pem"))
	if err != nil {
		return fmt.Errorf("loading server SVID: %w", err)
	}
	clientSVID, err := x509svid.Load(
		filepath.Join(deliveryDir, "client_svid.pem"),
		filepath.Join(deliveryDir, "client_key.pem"))
	if err != nil {
		return fmt.Errorf("loading client SVID: %w", err)
	}
	bundle, err := x509bundle.Load(td, filepath.Join(deliveryDir, "bundle.pem"))
	if err != nil {
		return fmt.Errorf("loading bundle: %w", err)
	}

	serverConfig := tlsconfig.MTLSServerConfig(serverSVID, bundle, tlsconfig.AuthorizeID(clientID))
	clientConfig := tlsconfig.MTLSClientConfig(clientSVID, bundle, tlsconfig.AuthorizeID(serverID))

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverConfig)
	if err != nil {
		return err
	}
	defer listener.Close()

	type serverResult struct {
		peer spiffeid.ID
		err  error
	}
	results := make(chan serverResult, 1)
	go func() {
		peer, err := serveOnce(listener)
		results <- serverResult{peer: peer, err: err}
	}()

	conn, err := tls.Dial("tcp", listener.Addr().String(), clientConfig)
	if err != nil {
		return fmt.Errorf("client dial: %w", err)
	}
	defer conn.Close()

	greeting, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("client read: %w", err)
	}
	serverPeer, err := peerID(conn.ConnectionState())
	if err != nil {
		return err
	}

	result := <-results
	if result.err != nil {
		return fmt.Errorf("server: %w", result.err)
	}

	fmt.Printf("client received %q and authenticated the server as %s\n", greeting, serverPeer)
	fmt.Printf("server authenticated the client as %s\n", result.peer)
	return nil
}

// serveOnce accepts a single mTLS connection, returns the authenticated
// client SPIFFE ID, and sends a greeting.
func serveOnce(listener net.Listener) (spiffeid.ID, error) {
	conn, err := listener.Accept()
	if err != nil {
		return spiffeid.ID{}, err
	}
	defer conn.Close()

	tlsConn := conn.(*tls.Conn)
	if err := tlsConn.Handshake(); err != nil {
		return spiffeid.ID{}, fmt.Errorf("handshake: %w", err)
	}
	peer, err := peerID(tlsConn.ConnectionState())
	if err != nil {
		return spiffeid.ID{}, err
	}
	if _, err := fmt.Fprintln(tlsConn, "hello over SPIFFE mTLS"); err != nil {
		return spiffeid.ID{}, err
	}
	return peer, nil
}

func peerID(state tls.ConnectionState) (spiffeid.ID, error) {
	if len(state.PeerCertificates) == 0 {
		return spiffeid.ID{}, fmt.Errorf("no peer certificates")
	}
	return x509svid.IDFromCert(state.PeerCertificates[0])
}
