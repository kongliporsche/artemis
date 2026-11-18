// The CoreDevice userspace transport is adapted from Pinky's Apache-2.0
// internal/driver/ios/tunnel.go (https://github.com/DIYgod/pinky).
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

type deviceTunnel struct {
	conn        io.ReadWriteCloser
	listener    net.Listener
	stack       *stack.Stack
	link        *channel.Endpoint
	host, phone netip.Addr
	rsdPort     uint16
	ctx         context.Context
	cancel      context.CancelFunc
	closeOnce   sync.Once
}

func openDeviceTunnel(ctx context.Context, entry ios.DeviceEntry) (*deviceTunnel, error) {
	conn, err := ios.ConnectToService(entry, "com.apple.internal.devicecompute.CoreDeviceProxy")
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	ok := false
	defer func() {
		if !ok {
			stop()
			conn.Close()
		}
	}()
	req := []byte(`{"type":"clientHandshakeRequest","mtu":1280}`)
	h := []byte("CDTunnel\x00\x00")
	binary.BigEndian.PutUint16(h[8:], uint16(len(req)))
	if _, err := conn.Write(append(h, req...)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, h); err != nil {
		return nil, err
	}
	if string(h[:8]) != "CDTunnel" {
		return nil, errors.New("invalid CoreDevice tunnel response header")
	}
	resp := make([]byte, binary.BigEndian.Uint16(h[8:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	var p struct {
		ServerAddress    netip.Addr
		ServerRSDPort    uint16
		ClientParameters struct {
			Address netip.Addr
			MTU     uint32
		}
	}
	if err := json.Unmarshal(resp, &p); err != nil {
		return nil, err
	}
	if !p.ClientParameters.Address.Is6() || !p.ServerAddress.Is6() || p.ServerRSDPort == 0 ||
		p.ClientParameters.MTU < 1280 || p.ClientParameters.MTU > 65535 {
		return nil, errors.New("invalid CoreDevice tunnel parameters")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	t := &deviceTunnel{
		conn: conn, listener: ln, host: p.ClientParameters.Address,
		phone: p.ServerAddress, rsdPort: p.ServerRSDPort,
		link: channel.New(512, p.ClientParameters.MTU, ""),
		stack: stack.New(stack.Options{
			NetworkProtocols:   []stack.NetworkProtocolFactory{ipv6.NewProtocol},
			TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		}),
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	if err := t.stack.CreateNIC(1, t.link); err != nil {
		t.Close()
		return nil, fmt.Errorf("create tunnel NIC: %s", err)
	}
	if err := t.stack.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv6.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom16(t.host.As16()), PrefixLen: 64},
	}, stack.AddressProperties{}); err != nil {
		t.Close()
		return nil, fmt.Errorf("set tunnel address: %s", err)
	}
	t.stack.SetRouteTable([]tcpip.Route{{Destination: header.IPv6EmptySubnet, NIC: 1}})
	if !stop() {
		t.Close()
		return nil, ctx.Err()
	}
	ok = true
	go t.readPackets()
	go t.writePackets()
	go t.forward()
	return t, nil
}

func (t *deviceTunnel) Close() {
	t.closeOnce.Do(func() {
		t.cancel()
		t.conn.Close()
		t.listener.Close()
		t.link.Close()
		t.stack.Close()
	})
}

func (t *deviceTunnel) readPackets() {
	defer t.Close()
	var hdr [40]byte
	for {
		if _, err := io.ReadFull(t.conn, hdr[:]); err != nil || hdr[0]>>4 != 6 {
			return
		}
		p := make([]byte, len(hdr)+int(binary.BigEndian.Uint16(hdr[4:6])))
		copy(p, hdr[:])
		if _, err := io.ReadFull(t.conn, p[len(hdr):]); err != nil {
			return
		}
		packet := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(p)})
		t.link.InjectInbound(ipv6.ProtocolNumber, packet)
		packet.DecRef()
	}
}

func (t *deviceTunnel) writePackets() {
	defer t.Close()
	for {
		p := t.link.ReadContext(t.ctx)
		if p == nil {
			return
		}
		b := p.ToBuffer()
		data := b.Flatten()
		_, err := t.conn.Write(data)
		b.Release()
		p.DecRef()
		if err != nil {
			return
		}
	}
}

func (t *deviceTunnel) forward() {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return
		}
		go t.forwardConnection(local)
	}
}

func (t *deviceTunnel) forwardConnection(local net.Conn) {
	defer local.Close()
	stop := context.AfterFunc(t.ctx, func() { local.Close() })
	defer stop()
	local.SetReadDeadline(time.Now().Add(2 * time.Second))
	var target [20]byte
	if _, err := io.ReadFull(local, target[:]); err != nil || netip.AddrFrom16([16]byte(target[:16])) != t.phone {
		return
	}
	port := binary.LittleEndian.Uint32(target[16:])
	if port == 0 || port > 65535 {
		return
	}
	local.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
	remote, err := gonet.DialContextTCP(ctx, t.stack, tunnelAddress(t.phone, uint16(port)), ipv6.ProtocolNumber)
	cancel()
	if err != nil {
		return
	}
	defer remote.Close()
	go func() {
		io.Copy(remote, local)
		remote.CloseWrite()
	}()
	io.Copy(local, remote)
}

func tunnelAddress(addr netip.Addr, port uint16) tcpip.FullAddress {
	return tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom16(addr.As16()), Port: port}
}

func (t *deviceTunnel) entry(ctx context.Context, entry ios.DeviceEntry) (ios.DeviceEntry, io.Closer, error) {
	entry.Address = t.phone.String()
	entry.UserspaceTUN = true
	entry.UserspaceTUNPort = t.listener.Addr().(*net.TCPAddr).Port
	rsd, err := ios.NewWithAddrPortDevice(entry.Address, int(t.rsdPort), entry)
	if err != nil {
		return entry, nil, err
	}
	stop := context.AfterFunc(ctx, func() { rsd.Close() })
	entry.Rsd, err = rsd.HandshakeAsHost()
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		rsd.Close()
		return entry, nil, err
	}
	return entry, &rsd, nil
}
