// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shopify/zk"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/test"
)

type acknowledgmentProxy struct {
	listener    net.Listener
	target      string
	armed       atomic.Bool
	offline     atomic.Bool
	nodePath    atomic.Pointer[string]
	dropped     chan struct{}
	wg          sync.WaitGroup
	mu          sync.Mutex
	connections []net.Conn
}

func newAcknowledgmentProxy(t *testing.T, target string) *acknowledgmentProxy {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxy := &acknowledgmentProxy{listener: listener, target: target, dropped: make(chan struct{}, 1)}
	proxy.wg.Add(1)
	go func() {
		defer proxy.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			proxy.mu.Lock()
			proxy.connections = append(proxy.connections, conn)
			proxy.mu.Unlock()
			proxy.wg.Add(1)
			go func() {
				defer proxy.wg.Done()
				proxy.forward(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		require.NoError(t, listener.Close())
		proxy.mu.Lock()
		for _, conn := range proxy.connections {
			_ = conn.Close()
		}
		proxy.mu.Unlock()
		proxy.wg.Wait()
	})
	return proxy
}

func readZooKeeperPacket(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > 1024*1024 {
		return nil, errors.New("test proxy packet exceeds its limit")
	}
	packet := make([]byte, 4+length)
	copy(packet, header)
	_, err := io.ReadFull(conn, packet[4:])
	return packet, err
}

func (p *acknowledgmentProxy) forward(client net.Conn) {
	defer client.Close()
	if p.offline.Load() {
		return
	}
	dialer := net.Dialer{Timeout: time.Second}
	upstream, err := dialer.DialContext(context.Background(), "tcp", p.target)
	if err != nil {
		return
	}
	defer upstream.Close()
	var lostXID atomic.Int32
	requestsDone := make(chan struct{})
	go func() {
		defer close(requestsDone)
		defer upstream.Close()
		p.forwardRequests(client, upstream, &lostXID)
	}()
	defer func() { _ = client.Close(); <-requestsDone }()
	for {
		packet, err := readZooKeeperPacket(upstream)
		if err != nil {
			return
		}
		if len(packet) >= 8 && lostXID.Load() != 0 && int32(binary.BigEndian.Uint32(packet[4:8])) == lostXID.Load() {
			p.dropped <- struct{}{}
			return
		}
		if _, err := client.Write(packet); err != nil {
			return
		}
	}
}

func (p *acknowledgmentProxy) forwardRequests(client, upstream net.Conn, lostXID *atomic.Int32) {
	for {
		packet, err := readZooKeeperPacket(client)
		if err != nil {
			return
		}
		if p.isTargetWrite(packet) && p.armed.Swap(false) {
			lostXID.Store(int32(binary.BigEndian.Uint32(packet[4:8])))
		}
		if _, err := upstream.Write(packet); err != nil {
			return
		}
	}
}

func (p *acknowledgmentProxy) isTargetWrite(packet []byte) bool {
	target := p.nodePath.Load()
	if len(packet) < 16 || target == nil {
		return false
	}
	operation := int32(binary.BigEndian.Uint32(packet[8:12]))
	length := int(binary.BigEndian.Uint32(packet[12:16]))
	return (operation == 1 || operation == 5) && length <= len(packet)-16 &&
		string(packet[16:16+length]) == *target
}

func TestZooKeeperLostWriteAcknowledgment(t *testing.T) {
	ensemble := test.SetupZooKeeper(t)
	for _, node := range []string{preparedNode, activatedNode} {
		t.Run(node, func(t *testing.T) {
			proxy := newAcknowledgmentProxy(t, ensemble.Addresses[0])
			client := testZooKeeperClient(t, []string{proxy.listener.Addr().String()})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			missing, err := client.read(ctx, node)
			require.NoError(t, err)
			expected := missing
			if node == preparedNode {
				expected, _, err = client.write(ctx, node, testDescriptor(time.Now(), 0), missing)
				require.NoError(t, err)
			}
			value := testDescriptor(time.Now(), 1)
			target := client.config.BasePath + "/" + node
			proxy.nodePath.Store(&target)
			proxy.armed.Store(true)
			_, outcome, err := client.write(ctx, node, value, expected)
			require.Error(t, err, "server's success response is deliberately lost")
			require.Equal(t, writeUncertain, outcome)
			select {
			case <-proxy.dropped:
			case <-ctx.Done():
				t.Fatal("proxy did not intercept the write acknowledgment")
			}
			require.Eventually(t, client.available, 10*time.Second, 10*time.Millisecond)
			readBack, err := client.read(ctx, node)
			require.NoError(t, err)
			require.True(t, matchesWrite(readBack, expected, value))
		})
	}
}

func TestZooKeeperQueuedWriteAfterContextCancellation(t *testing.T) {
	ensemble := test.SetupZooKeeper(t)
	client := testZooKeeperClient(t, ensemble.Addresses)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	missing, err := client.read(ctx, preparedNode)
	require.NoError(t, err)
	conn, _, err := client.connection()
	require.NoError(t, err)
	initial, _, err := client.write(ctx, preparedNode, testDescriptor(time.Now(), 0), missing)
	require.NoError(t, err)
	for i := range 3 {
		ensemble.Pause(t, i)
	}
	require.Eventually(t, func() bool { return !client.available() }, 15*time.Second, 20*time.Millisecond)
	// Call the SDK deliberately, bypassing the publisher's fail-fast guard, to prove its queue semantics.
	value := testDescriptor(time.Now(), 1)
	payload, err := encodeDescriptor(value)
	require.NoError(t, err)
	canceled, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
	_, err = conn.Set(canceled, client.config.BasePath+"/"+preparedNode, payload, initial.version)
	stop()
	require.Error(t, err)
	for i := range 3 {
		ensemble.Resume(t, i)
	}
	require.Eventually(t, client.available, 30*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		node, err := client.read(ctx, preparedNode)
		return err == nil && sameDescriptor(node.value, value)
	}, 10*time.Second, 20*time.Millisecond, "context expiry must not be treated as rollback of the queued write")
}

func TestZooKeeperCancelledWriteRecoveryAfterNewSession(t *testing.T) {
	ensemble := test.SetupZooKeeper(t)
	proxy := newAcknowledgmentProxy(t, ensemble.Addresses[0])
	client := testZooKeeperClient(t, []string{proxy.listener.Addr().String()})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	missing, err := client.read(ctx, preparedNode)
	require.NoError(t, err)
	initial, _, err := client.write(ctx, preparedNode, testDescriptor(time.Now(), 0), missing)
	require.NoError(t, err)
	conn, session, err := client.connection()
	require.NoError(t, err)
	proxy.offline.Store(true)
	proxy.mu.Lock()
	for _, connection := range proxy.connections {
		_ = connection.Close()
	}
	proxy.mu.Unlock()
	require.Eventually(t, func() bool { return !client.available() }, 10*time.Second, 20*time.Millisecond)
	value := testDescriptor(time.Now(), 1)
	payload, err := encodeDescriptor(value)
	require.NoError(t, err)
	expired, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
	_, err = conn.Set(expired, client.config.BasePath+"/"+preparedNode, payload, initial.version)
	stop()
	require.Error(t, err)
	time.Sleep(2 * client.config.SessionTimeout)
	proxy.offline.Store(false)
	require.Eventually(t, func() bool {
		return client.available() && conn.SessionID() != session
	}, 30*time.Second, 20*time.Millisecond, "server must expire the disconnected original session")
	recoveryCtx, recoveryCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer recoveryCancel()
	current, err := client.read(recoveryCtx, preparedNode)
	require.NoError(t, err)
	if !matchesWrite(current, initial, value) {
		require.True(t, sameNode(initial, current), "only the original or exact expected transition is valid")
		expected := initial
		expected.session = current.session
		_, outcome, err := client.write(recoveryCtx, preparedNode, value, expected)
		if err != nil {
			require.ErrorIs(t, err, zk.ErrBadVersion)
			require.Equal(t, writeNotExecuted, outcome)
		} else {
			require.Equal(t, writeConfirmed, outcome)
		}
	}
	confirmed, err := client.read(recoveryCtx, preparedNode)
	require.NoError(t, err)
	require.True(t, matchesWrite(confirmed, initial, value), "retain the original CAS across session expiry")
}

func TestZooKeeperDNSRecoveryWhileMongoDBContinues(t *testing.T) {
	ensemble := test.SetupZooKeeper(t)
	host, port, err := net.SplitHostPort(ensemble.Addresses[0])
	require.NoError(t, err)
	addresses, err := net.LookupIP(host)
	require.NoError(t, err)
	var address net.IP
	for _, ip := range addresses {
		if ip.To4() != nil {
			address = ip.To4()
			break
		}
	}
	require.NotNil(t, address, "test DNS server requires an IPv4 Docker address")
	server, recovered := recoveryDNSServer(t, address)
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "udp", server.LocalAddr().String())
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
	client := newZooKeeperClient(t.Context(), config.SnapshotZooKeeper{
		Addresses: []string{net.JoinHostPort("quasar-recovery.test", port)},
		BasePath:  "/dns-recovery", SessionTimeout: time.Second,
	}, 100*time.Millisecond)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		require.NoError(t, client.close(ctx))
	})
	store := newFakeStore(t)
	w := newWorker(testConfig(), store)
	w.publication = newZooKeeperPublication(client, time.Millisecond)
	require.NoError(t, w.refreshSnapshot(t.Context()))
	require.Error(t, w.progressZooKeeper(t.Context()))
	w.publication.degrade(errZooKeeperUnavailable)
	require.NoError(t, w.resolve(t.Context()))
	require.NotEmpty(t, store.current.Version.SnapshotID)
	recovered.Store(true)
	require.Eventually(t, client.available, 10*time.Second, 10*time.Millisecond)
	require.NoError(t, w.progressZooKeeper(t.Context()))
	time.Sleep(time.Millisecond)
	require.NoError(t, w.progressZooKeeper(t.Context()))
	require.True(t, sameDescriptor(store.current.Version, w.publication.observed.activated.value))
}

func recoveryDNSServer(t *testing.T, address net.IP) (net.PacketConn, *atomic.Bool) {
	t.Helper()
	var lc net.ListenConfig
	server, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	require.NoError(t, err)
	recovered := &atomic.Bool{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, peer, err := server.ReadFrom(buffer)
			if err != nil {
				return
			}
			response := recoveryDNSResponse(buffer[:n], recovered.Load(), address)
			if response != nil {
				_, _ = server.WriteTo(response, peer)
			}
		}
	}()
	t.Cleanup(func() { require.NoError(t, server.Close()); <-done })
	return server, recovered
}

func recoveryDNSResponse(data []byte, available bool, address net.IP) []byte {
	if len(data) < 16 {
		return nil
	}
	end := 12
	for end < len(data) && data[end] != 0 {
		end += int(data[end]) + 1
	}
	end += 5
	if end > len(data) {
		return nil
	}
	response := append([]byte(nil), data[:end]...)
	response[10], response[11] = 0, 0
	response[2], response[3] = 0x81, 0x83
	if available {
		response[3] = 0x80
		if binary.BigEndian.Uint16(response[end-4:end-2]) == 1 {
			response[7] = 1
			response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 1, 0, 4)
			response = append(response, address...)
		}
	}
	return response
}
