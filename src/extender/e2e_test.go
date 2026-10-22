package main

// End-to-end protocol test.
//
// The test:
//   1. generates a listener profile exactly like GenerateProfiles(),
//   2. calls BuildPayload() to cross-compile a real Linux agent,
//   3. starts an in-process BeaconHTTP listener that implements the same
//      handshake as adapters/beacon_listener_http,
//   4. runs the agent and drives every cross-platform command through
//      CreateCommand -> PackTasks -> wire -> ProcessData.
//
// Requires a Go toolchain on PATH (the same one running the test).

import (
	"bytes"
	"crypto/rc4"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Adaptix-Framework/axc2"
)

// ---------------------------------------------------------------------------
// Fake teamserver
// ---------------------------------------------------------------------------

type fakeTeamserver struct {
	listener *fakeListener
}

func (f *fakeTeamserver) TsListenerInteralHandler(watermark string, data []byte) (string, error) {
	return "", fmt.Errorf("pivoting not supported")
}
func (f *fakeTeamserver) TsAgentProcessData(agentId string, bodyData []byte) error { return nil }
func (f *fakeTeamserver) TsAgentUpdateData(newAgentData adaptix.AgentData) error   { return nil }
func (f *fakeTeamserver) TsAgentTerminate(agentId string, terminateTaskId string) error {
	f.listener.terminated <- terminateTaskId
	return nil
}
func (f *fakeTeamserver) TsAgentUpdateDataPartial(agentId string, updateData interface{}) error {
	return nil
}
func (f *fakeTeamserver) TsAgentBuildExecute(builderId string, workingDir string, program string, args ...string) error {
	cmd := exec.Command(program, args...)
	cmd.Dir = workingDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, stderr.String())
	}
	return nil
}
func (f *fakeTeamserver) TsAgentBuildLog(builderId string, status int, message string) error {
	return nil
}
func (f *fakeTeamserver) TsAgentConsoleOutput(agentId string, messageType int, message string, clearText string, store bool) {
}
func (f *fakeTeamserver) TsPivotCreate(pivotId string, pAgentId string, chAgentId string, pivotName string, isRestore bool) error {
	return nil
}
func (f *fakeTeamserver) TsGetPivotInfoByName(pivotName string) (string, string, string) {
	return "", "", ""
}
func (f *fakeTeamserver) TsGetPivotInfoById(pivotId string) (string, string, string) {
	return "", "", ""
}
func (f *fakeTeamserver) TsPivotDelete(pivotId string) error { return nil }

func (f *fakeTeamserver) TsTaskCreate(agentId string, cmdline string, client string, taskData adaptix.TaskData) {
	f.listener.enqueue(taskData)
}
func (f *fakeTeamserver) TsTaskUpdate(agentId string, data adaptix.TaskData) {
	select {
	case f.listener.updates <- data:
	default:
	}
}
func (f *fakeTeamserver) TsTaskGetAvailableAll(agentId string, availableSize int) ([]adaptix.TaskData, error) {
	return nil, nil
}

func (f *fakeTeamserver) TsDownloadAdd(agentId string, fileId string, fileName string, fileSize int64) error {
	f.listener.dlStates <- fmt.Sprintf("start %s %s %d", fileId, fileName, fileSize)
	return nil
}
func (f *fakeTeamserver) TsDownloadUpdate(fileId string, state int, data []byte) error {
	f.listener.recordDownload(fileId, data)
	return nil
}
func (f *fakeTeamserver) TsDownloadClose(fileId string, reason int) error {
	f.listener.dlStates <- fmt.Sprintf("close %s %d", fileId, reason)
	return nil
}
func (f *fakeTeamserver) TsDownloadSave(agentId string, fileId string, filename string, content []byte) error {
	f.listener.recordDownload(fileId, content)
	return nil
}
func (f *fakeTeamserver) TsScreenshotAdd(agentId string, Note string, Content []byte) error {
	return nil
}

func (f *fakeTeamserver) TsClientGuiDisksWindows(taskData adaptix.TaskData, drives []adaptix.ListingDrivesDataWin) {
	f.listener.mu.Lock()
	f.listener.lastDrives = drives
	f.listener.mu.Unlock()
}
func (f *fakeTeamserver) TsClientGuiFilesStatus(taskData adaptix.TaskData) {}
func (f *fakeTeamserver) TsClientGuiFilesWindows(taskData adaptix.TaskData, path string, files []adaptix.ListingFileDataWin) {
}
func (f *fakeTeamserver) TsClientGuiFilesUnix(taskData adaptix.TaskData, path string, files []adaptix.ListingFileDataUnix) {
	f.listener.mu.Lock()
	f.listener.lastFiles = files
	f.listener.lastPath = path
	f.listener.mu.Unlock()
}
func (f *fakeTeamserver) TsClientGuiProcessWindows(taskData adaptix.TaskData, process []adaptix.ListingProcessDataWin) {
}
func (f *fakeTeamserver) TsClientGuiProcessUnix(taskData adaptix.TaskData, process []adaptix.ListingProcessDataUnix) {
	f.listener.mu.Lock()
	f.listener.lastProcs = process
	f.listener.mu.Unlock()
}

func (f *fakeTeamserver) TsTunnelStart(TunnelId string) (string, error) {
	return "00000001", nil
}
func (f *fakeTeamserver) TsTunnelCreateSocks4(AgentId string, Info string, Lhost string, Lport int) (string, error) {
	return "00000002", nil
}
func (f *fakeTeamserver) TsTunnelCreateSocks5(AgentId string, Info string, Lhost string, Lport int, UseAuth bool, Username string, Password string) (string, error) {
	return "00000003", nil
}
func (f *fakeTeamserver) TsTunnelCreateLportfwd(AgentId string, Info string, Lhost string, Lport int, Thost string, Tport int) (string, error) {
	return "00000004", nil
}
func (f *fakeTeamserver) TsTunnelCreateRportfwd(AgentId string, Info string, Lport int, Thost string, Tport int) (string, error) {
	return "00000005", nil
}
func (f *fakeTeamserver) TsTunnelUpdateRportfwd(tunnelId int, result bool) (string, string, error) {
	return "00000005", "reverse port forward updated", nil
}
func (f *fakeTeamserver) TsTunnelStopSocks(AgentId string, Port int)            {}
func (f *fakeTeamserver) TsTunnelStopLportfwd(AgentId string, Port int)         {}
func (f *fakeTeamserver) TsTunnelStopRportfwd(AgentId string, Port int)         {}
func (f *fakeTeamserver) TsTunnelConnectionClose(channelId int, writeOnly bool) {}
func (f *fakeTeamserver) TsTunnelConnectionHalt(channelId int, errorCode byte)  {}
func (f *fakeTeamserver) TsTunnelConnectionResume(AgentId string, channelId int, ioDirect bool) {
}
func (f *fakeTeamserver) TsTunnelConnectionData(channelId int, data []byte) {
	f.listener.tunnelData <- string(data)
}
func (f *fakeTeamserver) TsTunnelConnectionAccept(tunnelId int, channelId int) {}
func (f *fakeTeamserver) TsTunnelPause(channelId int)                          {}
func (f *fakeTeamserver) TsTunnelResume(channelId int)                         {}

func (f *fakeTeamserver) TsTerminalConnExists(terminalId string) bool { return false }
func (f *fakeTeamserver) TsTerminalGetPipe(AgentId string, terminalId string) (*io.PipeReader, *io.PipeWriter, error) {
	return nil, nil, fmt.Errorf("not implemented")
}
func (f *fakeTeamserver) TsTerminalConnResume(agentId string, terminalId string, ioDirect bool) {
	select {
	case f.listener.termOpen <- terminalId:
	default:
	}
}
func (f *fakeTeamserver) TsTerminalConnData(terminalId string, data []byte) {
	f.listener.termData <- string(data)
}
func (f *fakeTeamserver) TsTerminalConnClose(terminalId string, status string) error {
	select {
	case f.listener.termClose <- status:
	default:
	}
	return nil
}

func (f *fakeTeamserver) TsConvertCpToUTF8(input string, codePage int) string { return input }
func (f *fakeTeamserver) TsConvertUTF8toCp(input string, codePage int) string { return input }
func (f *fakeTeamserver) TsWin32Error(errorCode uint) string                  { return posixErrString(int(errorCode)) }

// ---------------------------------------------------------------------------
// Fake BeaconHTTP listener
// ---------------------------------------------------------------------------

type listenerConfig struct {
	URI        string `json:"uri"`
	Method     string `json:"http_method"`
	Header     string `json:"hb_header"`
	UserAgent  string `json:"user_agent"`
	Page       string `json:"page-payload"`
	EncryptKey string `json:"encrypt_key"`
	Protocol   string `json:"protocol"`
}

type fakeListener struct {
	mu         sync.Mutex
	key        []byte
	cfg        listenerConfig
	pagePrefix string
	pageSuffix string
	plugin     *PluginAgent
	ext        adaptix.ExtenderAgent
	agent      adaptix.AgentData
	created    bool
	queue      []adaptix.TaskData

	updates    chan adaptix.TaskData
	downloads  map[string]*bytes.Buffer
	dlStates   chan string
	termOpen   chan string
	termData   chan string
	termClose  chan string
	tunnelData chan string
	terminated chan string
	registered chan struct{}
	regOnce    sync.Once

	lastFiles  []adaptix.ListingFileDataUnix
	lastPath   string
	lastProcs  []adaptix.ListingProcessDataUnix
	lastDrives []adaptix.ListingDrivesDataWin

	srv *http.Server
}

func newFakeListener(key []byte, cfg listenerConfig, page string) *fakeListener {
	idx := strings.Index(page, "<<<PAYLOAD_DATA>>>")
	return &fakeListener{
		key:        key,
		cfg:        cfg,
		pagePrefix: page[:idx],
		pageSuffix: page[idx+len("<<<PAYLOAD_DATA>>>"):],
		downloads:  make(map[string]*bytes.Buffer),
		updates:    make(chan adaptix.TaskData, 512),
		dlStates:   make(chan string, 64),
		termOpen:   make(chan string, 16),
		termData:   make(chan string, 64),
		termClose:  make(chan string, 16),
		tunnelData: make(chan string, 64),
		terminated: make(chan string, 4),
		registered: make(chan struct{}),
	}
}

func (l *fakeListener) enqueue(td adaptix.TaskData) {
	if td.TaskId == "" {
		td.TaskId = fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	l.mu.Lock()
	l.queue = append(l.queue, td)
	l.mu.Unlock()
}

func (l *fakeListener) takeQueue() []adaptix.TaskData {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.queue
	l.queue = nil
	return out
}

func (l *fakeListener) recordDownload(id string, data []byte) {
	l.mu.Lock()
	buf := l.downloads[id]
	if buf == nil {
		buf = &bytes.Buffer{}
		l.downloads[id] = buf
	}
	buf.Write(data)
	l.mu.Unlock()
}

func (l *fakeListener) downloaded(id string) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.downloads[id]; b != nil {
		return append([]byte(nil), b.Bytes()...)
	}
	return nil
}

func (l *fakeListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != l.cfg.URI || r.UserAgent() != l.cfg.UserAgent {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
		return
	}

	beatB64 := r.Header.Get(l.cfg.Header)
	if beatB64 == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rawBeat, err := base64.StdEncoding.DecodeString(beatB64)
	if err != nil || len(rawBeat) < 9 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	c, _ := rc4.NewCipher(l.key)
	beat := make([]byte, len(rawBeat))
	c.XORKeyStream(beat, rawBeat)
	agentID := fmt.Sprintf("%08x", binary.BigEndian.Uint32(beat[4:8]))

	body, _ := io.ReadAll(r.Body)

	l.mu.Lock()
	if !l.created {
		agentData, ext, err := l.plugin.CreateAgent(beat[8:])
		if err != nil {
			l.mu.Unlock()
			fmt.Printf("[listener] CreateAgent error: %v (beat len %d)\n", err, len(beat)-8)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		agentData.Id = agentID
		l.agent = agentData
		l.ext = ext
		l.created = true
		l.regOnce.Do(func() { close(l.registered) })
	}
	agentData := l.agent
	ext := l.ext
	l.mu.Unlock()

	if len(body) > 0 {
		plain, err := ext.Decrypt(body, agentData.SessionKey)
		if err == nil && len(plain) > 0 {
			_ = ext.ProcessData(agentData, plain)
		}
	}

	tasks := l.takeQueue()
	payload := ""
	if len(tasks) > 0 {
		plain, err := ext.PackTasks(agentData, tasks)
		if err == nil {
			enc, _ := ext.Encrypt(plain, agentData.SessionKey)
			payload = string(enc)
		}
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(l.pagePrefix + payload + l.pageSuffix))
}

// waitUpdates collects task updates until the stream goes quiet.
func (l *fakeListener) waitUpdates(t *testing.T, quiet time.Duration) []adaptix.TaskData {
	t.Helper()
	var out []adaptix.TaskData
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case td := <-l.updates:
			out = append(out, td)
		case <-time.After(quiet):
			if len(out) == 0 && time.Now().Before(deadline) {
				continue
			}
			return out
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func buildListenerJSON(t *testing.T, port int) ([]byte, []byte, listenerConfig) {
	t.Helper()
	cfg := listenerConfig{
		URI:        "/api/v1/poll",
		Method:     "POST",
		Header:     "X-Session-Id",
		UserAgent:  "JinxedE2E/1.0",
		Page:       "<html><body><div id=\"app\"><<<PAYLOAD_DATA>>></div></body></html>",
		EncryptKey: "00112233445566778899aabbccddeeff",
		Protocol:   "http",
	}

	raw := map[string]any{
		"protocol":           "http",
		"encrypt_key":        cfg.EncryptKey,
		"callback_addresses": []interface{}{fmt.Sprintf("127.0.0.1:%d", port)},
		"uri":                []interface{}{cfg.URI},
		"http_method":        cfg.Method,
		"hb_header":          cfg.Header,
		"user_agent":         []interface{}{cfg.UserAgent},
		"host_header":        []interface{}{},
		"request_headers":    "X-Jinxed: e2e\r\n",
		"ssl":                false,
		"page-payload":       cfg.Page,
		"rotation_mode":      "sequential",
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	key, _ := hexDecode(cfg.EncryptKey)
	return blob, key, cfg
}

func hexDecode(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

func cmdArgs(kv ...any) map[string]any {
	m := map[string]any{"message": ""}
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		m[key] = kv[i+1]
	}
	return m
}

// ---------------------------------------------------------------------------
// The test
// ---------------------------------------------------------------------------

func TestEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	port := freePort(t)
	listenerBlob, listenerKey, lcfg := buildListenerJSON(t, port)

	plugin := &PluginAgent{}
	ts := &fakeTeamserver{}
	fl := newFakeListener(listenerKey, lcfg, lcfg.Page)
	ts.listener = fl
	fl.plugin = plugin

	// Wire the plugin exactly like InitPlugin() does.
	ModuleDir = ""
	AgentWatermark = "c0de11fe"
	Ts = ts

	buildProfile := adaptix.BuildProfile{
		BuilderId: "",
		AgentConfig: `{"arch":"amd64","format":"ELF","sleep":"0","jitter":0,` +
			`"is_killdate":false,"kill_date":"","kill_time":"",` +
			`"is_workingtime":false,"start_time":"","end_time":"","debug":false}`,
		ListenerProfiles: []adaptix.TransportProfile{
			{Watermark: "aaaa1111", Profile: listenerBlob},
		},
	}

	profiles, err := plugin.GenerateProfiles(buildProfile)
	if err != nil {
		t.Fatalf("GenerateProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}

	payload, filename, err := plugin.BuildPayload(buildProfile, profiles)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if len(payload) < 1000 || payload[0] != 0x7f || string(payload[1:4]) != "ELF" {
		t.Fatalf("payload is not a valid ELF (%d bytes)", len(payload))
	}
	t.Logf("built %s: %d bytes", filename, len(payload))

	tmp := t.TempDir()
	agentPath := filepath.Join(tmp, "agent")
	if savePath := os.Getenv("JINXED_E2E_SAVE"); savePath != "" {
		if err := os.WriteFile(savePath, payload, 0755); err != nil {
			t.Fatal(err)
		}
		t.Logf("saved payload to %s; port=%d uri=%s", savePath, port, lcfg.URI)
		return
	}
	if err := os.WriteFile(agentPath, payload, 0755); err != nil {
		t.Fatal(err)
	}

	// Start the listener.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: fl}
	fl.srv = srv
	go func() { _ = srv.Serve(ln) }()
	t.Logf("listener up on %s", ln.Addr().String())
	defer srv.Close()

	// Start the agent.
	cmd := exec.Command(agentPath, "-debug")
	var agentErr bytes.Buffer
	cmd.Stderr = &agentErr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() && agentErr.Len() > 0 {
			t.Logf("agent stderr:\n%s", agentErr.String())
		}
	}()

	select {
	case <-fl.registered:
	case <-time.After(20 * time.Second):
		t.Fatalf("agent did not register (stderr: %s)", agentErr.String())
	}

	fl.mu.Lock()
	agentData := fl.agent
	fl.mu.Unlock()

	if agentData.Os != adaptix.OS_LINUX {
		t.Fatalf("expected OS_LINUX, got %d", agentData.Os)
	}
	if agentData.Computer == "" || agentData.Username == "" {
		t.Fatalf("registration missing hostname/username: %+v", agentData)
	}
	if len(agentData.SessionKey) != 16 {
		t.Fatalf("bad session key length")
	}
	t.Logf("session: %s %s %s %s", agentData.Computer, agentData.Username, agentData.OsDesc, agentData.Arch)

	ext := fl.ext

	// execCmd sends one command and returns all resulting updates.
	execCmd := func(a map[string]any) []adaptix.TaskData {
		t.Helper()
		td, _, err := ext.CreateCommand(agentData, a)
		if err != nil {
			t.Fatalf("CreateCommand(%v): %v", a["command"], err)
		}
		if len(td.Data) > 0 {
			fl.enqueue(td)
		}
		return fl.waitUpdates(t, 500*time.Millisecond)
	}

	expectMessage := func(updates []adaptix.TaskData, substr string) adaptix.TaskData {
		t.Helper()
		for _, u := range updates {
			if strings.Contains(u.Message, substr) || strings.Contains(u.ClearText, substr) {
				return u
			}
		}
		t.Fatalf("no update containing %q; got %+v", substr, summarize(updates))
		return adaptix.TaskData{}
	}

	// --- basic metadata ---------------------------------------------------
	updates := execCmd(cmdArgs("command", "getuid"))
	expectMessage(updates, "You are")

	updates = execCmd(cmdArgs("command", "uname"))
	uname := expectMessage(updates, "System information:")
	if !strings.Contains(uname.ClearText, "Linux") {
		t.Fatalf("uname output missing Linux: %q", uname.ClearText)
	}

	updates = execCmd(cmdArgs("command", "sysinfo"))
	sysinfo := expectMessage(updates, "Host information:")
	for _, want := range []string{"Hostname", "Kernel", "Uptime", "CPUs"} {
		if !strings.Contains(sysinfo.ClearText, want) {
			t.Fatalf("sysinfo missing %q: %q", want, sysinfo.ClearText)
		}
	}

	updates = execCmd(cmdArgs("command", "disks"))
	expectMessage(updates, "mounted filesystems")
	fl.mu.Lock()
	if len(fl.lastDrives) == 0 {
		t.Fatal("disks did not populate the GUI drive list")
	}
	fl.mu.Unlock()

	updates = execCmd(cmdArgs("command", "ifconfig"))
	expectMessage(updates, "interfaces")

	updates = execCmd(cmdArgs("command", "netstat"))
	expectMessage(updates, "Sockets")

	updates = execCmd(cmdArgs("command", "users"))
	// users may legitimately be empty; just ensure the command answered
	if len(updates) == 0 {
		t.Fatal("users produced no update")
	}

	// --- filesystem -------------------------------------------------------
	tmpDir := filepath.Join(tmp, "work")
	if err := os.MkdirAll(filepath.Join(tmpDir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(tmpDir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("jinxed-e2e-payload"), 0644); err != nil {
		t.Fatal(err)
	}

	updates = execCmd(cmdArgs("command", "pwd"))
	expectMessage(updates, "Current working directory")

	updates = execCmd(cmdArgs("command", "cd", "path", tmpDir))
	expectMessage(updates, tmpDir)

	updates = execCmd(cmdArgs("command", "ls", "path", tmpDir))
	lsTask := expectMessage(updates, "Listing")
	if !strings.Contains(lsTask.ClearText, "hello.txt") {
		t.Fatalf("ls output missing hello.txt: %q", lsTask.ClearText)
	}
	fl.mu.Lock()
	if len(fl.lastFiles) < 2 {
		t.Fatalf("expected unix GUI file entries, got %d", len(fl.lastFiles))
	}
	fl.mu.Unlock()

	updates = execCmd(cmdArgs("command", "cat", "path", testFile))
	catTask := expectMessage(updates, "file content")
	if catTask.ClearText != "jinxed-e2e-payload" {
		t.Fatalf("cat mismatch: %q", catTask.ClearText)
	}

	copyPath := filepath.Join(tmpDir, "copy.txt")
	execCmd(cmdArgs("command", "cp", "src", testFile, "dst", copyPath))
	if data, err := os.ReadFile(copyPath); err != nil || string(data) != "jinxed-e2e-payload" {
		t.Fatalf("cp failed: %v", err)
	}

	movedPath := filepath.Join(tmpDir, "moved.txt")
	execCmd(cmdArgs("command", "mv", "src", copyPath, "dst", movedPath))
	if _, err := os.Stat(movedPath); err != nil {
		t.Fatalf("mv failed: %v", err)
	}

	newDir := filepath.Join(tmpDir, "created", "nested")
	updates = execCmd(cmdArgs("command", "mkdir", "path", newDir))
	expectMessage(updates, "created successfully")
	if fi, err := os.Stat(newDir); err != nil || !fi.IsDir() {
		t.Fatalf("mkdir failed: %v", err)
	}

	chmodPath := filepath.Join(tmpDir, "mode.txt")
	_ = os.WriteFile(chmodPath, []byte("x"), 0644)
	updates = execCmd(cmdArgs("command", "chmod", "path", chmodPath, "mode", "0600"))
	expectMessage(updates, "Mode of")
	if fi, err := os.Stat(chmodPath); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("chmod failed: %v %v", err, fi.Mode())
	}

	updates = execCmd(cmdArgs("command", "rm", "path", movedPath))
	expectMessage(updates, "deleted successfully")
	if _, err := os.Stat(movedPath); !os.IsNotExist(err) {
		t.Fatalf("rm failed")
	}

	// --- environment ------------------------------------------------------
	updates = execCmd(cmdArgs("command", "env", "subcommand", "list"))
	envList := expectMessage(updates, "Environment")
	if !strings.Contains(envList.ClearText, "PATH=") {
		t.Fatalf("env list missing PATH")
	}

	execCmd(cmdArgs("command", "env", "subcommand", "set", "name", "JINXED_TEST", "value", "42"))
	updates = execCmd(cmdArgs("command", "env", "subcommand", "get", "name", "JINXED_TEST"))
	envGet := expectMessage(updates, "Variable value")
	if envGet.ClearText != "42" {
		t.Fatalf("env get mismatch: %q", envGet.ClearText)
	}
	execCmd(cmdArgs("command", "env", "subcommand", "unset", "name", "JINXED_TEST"))
	updates = execCmd(cmdArgs("command", "env", "subcommand", "get", "name", "JINXED_TEST"))
	expectMessage(updates, "not set")

	// --- processes --------------------------------------------------------
	updates = execCmd(cmdArgs("command", "ps", "subcommand", "list"))
	psTask := expectMessage(updates, "Process list")
	if !strings.Contains(psTask.ClearText, "PID") {
		t.Fatalf("ps output malformed")
	}
	fl.mu.Lock()
	if len(fl.lastProcs) == 0 {
		t.Fatal("ps list did not populate the unix GUI process list")
	}
	fl.mu.Unlock()

	updates = execCmd(cmdArgs("command", "ps", "subcommand", "run", "-o", true, "args", "/bin/echo jinxed-process-output"))
	runTask := expectMessage(updates, "Program")
	if !strings.Contains(runTask.Message, "started with PID") {
		t.Fatalf("ps run message: %q", runTask.Message)
	}
	// Job output arrives on later beacons; it may already be part of updates.
	jobOut := append(updates, fl.waitUpdates(t, 1*time.Second)...)
	found := false
	for _, u := range jobOut {
		if strings.Contains(u.ClearText, "jinxed-process-output") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ps run output missing; got %+v", summarize(jobOut))
	}

	updates = execCmd(cmdArgs("command", "jobs", "subcommand", "list"))
	if len(updates) == 0 {
		t.Fatal("jobs list produced no update")
	}

	updates = execCmd(cmdArgs("command", "shell", "cmd_params", "echo shell-parity; id -u"))
	shellOut := append(updates, fl.waitUpdates(t, 1*time.Second)...)
	foundShell := false
	for _, u := range shellOut {
		if strings.Contains(u.ClearText, "shell-parity") {
			foundShell = true
		}
	}
	if !foundShell {
		t.Fatalf("shell output missing; got %+v", summarize(shellOut))
	}
	_ = updates

	// --- download ---------------------------------------------------------
	bigFile := filepath.Join(tmpDir, "big.bin")
	blob := bytes.Repeat([]byte("0123456789abcdef"), 20000) // 320 KB
	if err := os.WriteFile(bigFile, blob, 0644); err != nil {
		t.Fatal(err)
	}
	updates = execCmd(cmdArgs("command", "download", "file", bigFile))
	startTask := expectMessage(updates, "has started")
	if !strings.Contains(startTask.Message, "320000") {
		t.Fatalf("download size wrong: %q", startTask.Message)
	}
	// Collect the streamed chunks.
	time.Sleep(1500 * time.Millisecond)
	fl.waitUpdates(t, 300*time.Millisecond)

	fl.mu.Lock()
	var fileID string
	for id := range fl.downloads {
		fileID = id
	}
	fl.mu.Unlock()
	if fileID == "" {
		t.Fatal("no download recorded")
	}
	got := fl.downloaded(fileID)
	if !bytes.Equal(got, blob) {
		t.Fatalf("download mismatch: got %d bytes, want %d", len(got), len(blob))
	}

	// --- upload -----------------------------------------------------------
	uploadSrc := filepath.Join(tmp, "upload-src.txt")
	uploadDst := filepath.Join(tmpDir, "upload-dst.txt")
	uploadData := []byte("uploaded-by-jinxed-e2e\n")
	if err := os.WriteFile(uploadSrc, uploadData, 0644); err != nil {
		t.Fatal(err)
	}
	updates = execCmd(cmdArgs("command", "upload",
		"local_file", base64.StdEncoding.EncodeToString(uploadData),
		"remote_path", uploadDst))
	expectMessage(updates, "uploaded")
	if got, err := os.ReadFile(uploadDst); err != nil || !bytes.Equal(got, uploadData) {
		t.Fatalf("upload failed: %v", err)
	}

	// --- profile ----------------------------------------------------------
	updates = execCmd(cmdArgs("command", "profile", "subcommand", "killdate", "datetime", "0"))
	expectMessage(updates, "disabled")
	updates = execCmd(cmdArgs("command", "profile", "subcommand", "workingtime", "time", "0"))
	expectMessage(updates, "disabled")
	updates = execCmd(cmdArgs("command", "profile", "subcommand", "download.chunksize", "size", float64(65536)))
	expectMessage(updates, "chunk size")

	// --- remote terminal --------------------------------------------------
	termTask := ext.TerminalCallbacks().Start(0x11223344, "/bin/sh", 24, 80, 65001)
	fl.enqueue(termTask)
	select {
	case id := <-fl.termOpen:
		if id != "11223344" {
			t.Fatalf("terminal id mismatch: %s", id)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("terminal did not start")
	}

	writeTask := ext.TerminalCallbacks().Write(0x11223344, 65001, []byte("echo terminal-parity\n"))
	fl.enqueue(writeTask)
	deadline := time.After(10 * time.Second)
	termData := ""
	for !strings.Contains(termData, "terminal-parity") {
		select {
		case chunk := <-fl.termData:
			termData += chunk
		case <-deadline:
			t.Fatalf("terminal output missing: %q", termData)
		}
	}

	closeTask := ext.TerminalCallbacks().Close(0x11223344)
	fl.enqueue(closeTask)
	select {
	case <-fl.termClose:
	case <-time.After(10 * time.Second):
		t.Fatal("terminal did not close")
	}

	// --- terminate --------------------------------------------------------
	td, _, err := ext.CreateCommand(agentData, cmdArgs("command", "terminate", "subcommand", "process"))
	if err != nil {
		t.Fatal(err)
	}
	fl.enqueue(td)
	select {
	case <-fl.terminated:
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not terminate")
	}

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("agent process did not exit")
	}
}

func summarize(updates []adaptix.TaskData) []string {
	out := make([]string, 0, len(updates))
	for _, u := range updates {
		out = append(out, fmt.Sprintf("{%s|%s|%s}", u.Message, strings.TrimSpace(u.ClearText), u.TaskId))
	}
	return out
}

// TestBuildPayloadFromListener is a maintenance helper: it builds a payload
// against a listener profile dumped from a live teamserver, which is useful for
// validating a deployment without the GUI.
//
//	JINXED_LISTENER_JSON=/tmp/listener.json JINXED_AGENT_OUT=/tmp/agent \
//	  go test -run TestBuildPayloadFromListener -v .
func TestBuildPayloadFromListener(t *testing.T) {
	jsonPath := os.Getenv("JINXED_LISTENER_JSON")
	outPath := os.Getenv("JINXED_AGENT_OUT")
	if jsonPath == "" || outPath == "" {
		t.Skip("JINXED_LISTENER_JSON / JINXED_AGENT_OUT not set")
	}

	blob, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}

	plugin := &PluginAgent{}
	ModuleDir = ""
	AgentWatermark = "c0de11fe"
	Ts = &fakeTeamserver{listener: newFakeListener(nil, listenerConfig{}, "<html><<<PAYLOAD_DATA>>></html>")}

	arch := os.Getenv("JINXED_ARCH")
	if arch == "" {
		arch = "amd64"
	}

	bp := adaptix.BuildProfile{
		AgentConfig: fmt.Sprintf(`{"arch":%q,"format":"ELF","sleep":"1s","jitter":10}`, arch),
		ListenerProfiles: []adaptix.TransportProfile{
			{Watermark: "aaaa1111", Profile: blob},
		},
	}

	profiles, err := plugin.GenerateProfiles(bp)
	if err != nil {
		t.Fatalf("GenerateProfiles: %v", err)
	}
	payload, name, err := plugin.BuildPayload(bp, profiles)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if err := os.WriteFile(outPath, payload, 0755); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%s, %d bytes)", outPath, name, len(payload))
}
