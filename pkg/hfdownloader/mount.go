// SPDX-License-Identifier: Apache-2.0

package hfdownloader

// `hfdownloader mount`: one FUSE mount that shows every app its own layout of
// the two stores (huggingface hub+local, diffusion). Reads are served from the
// catalog (mount_catalog.go); writes land in an upper dir and are ingested
// into the right store when the writer closes the file. No link trees to keep
// in sync: hub/local changes are picked up by inotify and the catalog is
// rebuilt in memory.

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type mountFS struct {
	c     *HFCache
	opts  MountOptions
	upper string
	cat   atomic.Pointer[mountCatalog]

	wmu       sync.Mutex
	whiteouts map[string]bool

	ingestMu sync.Mutex // one ingest at a time
	pmu      sync.Mutex
	writers  map[string]int       // open write handles per upper path
	pending  map[string]time.Time // upper path -> last close

	hc       *hashCache
	hashQ    chan string
	hashing  sync.Map
	refreshC chan bool // true = also sort diffusion repos out of the hub
	uid, gid uint32
}

const whiteoutFile = ".hfd-whiteouts.json"

// Mount serves the views until ctx is cancelled.
func (c *HFCache) Mount(ctx context.Context, opts MountOptions) error {
	if opts.Watch.Settle == 0 {
		opts.Watch.Settle = 10 * time.Second
	}
	m := &mountFS{
		c: c, opts: opts, upper: opts.UpperDir,
		whiteouts: map[string]bool{}, writers: map[string]int{}, pending: map[string]time.Time{},
		hc:       loadHashCache(filepath.Join(c.Root, ".adopt-hashes.json")),
		hashQ:    make(chan string, 4096),
		refreshC: make(chan bool, 1),
		uid:      uint32(os.Getuid()), gid: uint32(os.Getgid()),
	}
	for _, v := range MountViews {
		if err := os.MkdirAll(filepath.Join(m.upper, v), 0755); err != nil {
			return err
		}
	}
	if b, err := os.ReadFile(filepath.Join(m.upper, whiteoutFile)); err == nil {
		json.Unmarshal(b, &m.whiteouts)
	}
	m.opts.Watch.Views = ViewsOptions{
		Root: m.upper, DiffusionDir: opts.DiffusionDir,
		MaestroDir: filepath.Join(m.upper, "maestro"),
	}
	m.refresh(opts.DiffusionDir != "")

	sec := time.Second
	root := &mnode{m: m}
	srv, err := fs.Mount(opts.MountPoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: opts.AllowOther, FsName: "hfd", Name: "hfd",
			MaxWrite: 1 << 20, MaxReadAhead: 1 << 20, Debug: opts.Debug,
		},
		EntryTimeout: &sec, AttrTimeout: &sec, NegativeTimeout: &sec,
		UID: m.uid, GID: m.gid,
	})
	if err != nil {
		return err
	}
	m.logf("mounted %s (upper %s): ollama=%d lmstudio=%d hipfire=%d",
		opts.MountPoint, m.upper, m.cat.Load().stats["ollama"], m.cat.Load().stats["lmstudio"], m.cat.Load().stats["hipfire"])

	go m.hasher(ctx)
	go m.refresher(ctx)
	go m.watchStores(ctx)
	m.queueExisting()

	go func() {
		<-ctx.Done()
		for i := 0; i < 50; i++ {
			if srv.Unmount() == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	srv.Wait()
	m.hc.save()
	return nil
}

func (m *mountFS) logf(f string, a ...any) { m.opts.Watch.logf(f, a...) }

// ---- catalog refresh ---------------------------------------------------------

func (m *mountFS) localSHA(p string) string {
	if h, ok := m.hc.get(p); ok && h.SHA256 != "" {
		return h.SHA256
	}
	if strings.HasSuffix(strings.ToLower(p), ".gguf") {
		if _, busy := m.hashing.LoadOrStore(p, true); !busy {
			select {
			case m.hashQ <- p:
			default:
				m.hashing.Delete(p)
			}
		}
	}
	return ""
}

func (m *mountFS) refresh(sort bool) {
	if sort && m.opts.DiffusionDir != "" {
		if _, err := m.c.SortDiffusion(m.opts.DiffusionDir, false, m.logf); err != nil {
			m.logf("sort diffusion: %v", err)
		}
	}
	hub, err := m.c.SnapshotFiles()
	if err != nil {
		m.logf("catalog: %v", err)
		hub = map[string][]HubFile{}
	}
	cat := buildMountCatalog(hub, localFiles(m.c.Root, m.localSHA))
	m.cat.Store(cat)
}

func (m *mountFS) kick(sort bool) {
	select {
	case m.refreshC <- sort:
	default:
		if sort { // make sure a pending plain refresh is upgraded
			select {
			case <-m.refreshC:
			default:
			}
			select {
			case m.refreshC <- true:
			default:
			}
		}
	}
}

func (m *mountFS) refresher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-m.refreshC:
			// let a burst of events settle
			t := time.NewTimer(3 * time.Second)
		drain:
			for {
				select {
				case s2 := <-m.refreshC:
					s = s || s2
				case <-t.C:
					break drain
				case <-ctx.Done():
					return
				}
			}
			old := m.cat.Load().stats
			m.refresh(s)
			n := m.cat.Load().stats
			if old["ollama"] != n["ollama"] || old["lmstudio"] != n["lmstudio"] || old["hipfire"] != n["hipfire"] {
				m.logf("catalog: ollama=%d lmstudio=%d hipfire=%d", n["ollama"], n["lmstudio"], n["hipfire"])
			}
		}
	}
}

// hasher computes sha256 of local/ GGUFs so they get Ollama digests.
func (m *mountFS) hasher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-m.hashQ:
			fi, err := os.Stat(p)
			if err == nil && time.Since(fi.ModTime()) > time.Minute {
				if h, err := hashLocal(p, fi.Size()); err == nil {
					m.hc.put(h)
					m.hc.save()
					m.kick(false)
				}
			}
			m.hashing.Delete(p)
		}
	}
}

// watchStores rebuilds the catalog when hub snapshots/refs or local/ change.
// blobs/ is not watched: download churn is not a change until refs move.
func (m *mountFS) watchStores(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		m.logf("inotify: %v", err)
		return
	}
	defer w.Close()
	hub := m.c.HubDir()
	if r, err := filepath.EvalSymlinks(hub); err == nil {
		hub = r
	}
	local := filepath.Join(m.c.Root, "local")
	if r, err := filepath.EvalSymlinks(local); err == nil {
		local = r
	}
	os.MkdirAll(local, 0755)
	add := func(dir string) {
		filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() {
				return nil
			}
			if n := info.Name(); n == "blobs" || n == ".locks" || n == ".git" {
				return filepath.SkipDir
			}
			w.Add(p)
			return nil
		})
	}
	add(hub)
	add(local)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					add(ev.Name)
				}
			}
			m.kick(strings.HasPrefix(ev.Name, hub+string(filepath.Separator)))
		case err := <-w.Errors:
			m.logf("inotify: %v", err)
		}
	}
}

// ---- ingest of written files ------------------------------------------------

func (m *mountFS) openedForWrite(p string) {
	m.pmu.Lock()
	m.writers[p]++
	m.pmu.Unlock()
}

func (m *mountFS) closedWrite(p string) {
	m.pmu.Lock()
	if m.writers[p]--; m.writers[p] <= 0 {
		delete(m.writers, p)
	}
	m.pending[p] = time.Now()
	m.pmu.Unlock()
	time.AfterFunc(m.opts.Watch.Settle, func() { m.tryIngest(p) })
}

func (m *mountFS) queueExisting() {
	filepath.Walk(m.upper, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && (info.Name() == ".cache" || info.Name() == "bin") {
			return filepath.SkipDir
		}
		if info.Mode().IsRegular() && ingestCandidate(p, info) {
			m.pmu.Lock()
			m.pending[p] = time.Time{}
			m.pmu.Unlock()
			go m.tryIngest(p)
		}
		return nil
	})
}

func (m *mountFS) tryIngest(p string) {
	m.pmu.Lock()
	last, ok := m.pending[p]
	busy := m.writers[p] > 0
	m.pmu.Unlock()
	if !ok || busy || time.Since(last) < m.opts.Watch.Settle-50*time.Millisecond {
		return
	}
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || !ingestCandidate(p, info) {
		m.pmu.Lock()
		delete(m.pending, p)
		m.pmu.Unlock()
		return
	}
	if time.Since(info.ModTime()) < m.opts.Watch.Settle {
		time.AfterFunc(m.opts.Watch.Settle, func() { m.tryIngest(p) })
		return
	}
	m.pmu.Lock()
	delete(m.pending, p)
	m.pmu.Unlock()
	m.ingestMu.Lock()
	changed := m.c.ingest(context.Background(), p, m.opts.Watch)
	m.ingestMu.Unlock()
	if changed {
		m.kick(true)
	}
}

// ---- whiteouts ----------------------------------------------------------------

func (m *mountFS) whited(rel string) bool {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	for r := rel; r != "" && r != "."; r = path.Dir(r) {
		if m.whiteouts[r] {
			return true
		}
	}
	return false
}

func (m *mountFS) setWhiteout(rel string, on bool) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if m.whiteouts[rel] == on {
		return
	}
	if on {
		m.whiteouts[rel] = true
	} else {
		delete(m.whiteouts, rel)
	}
	b, _ := json.Marshal(m.whiteouts)
	tmp := filepath.Join(m.upper, whiteoutFile+".tmp")
	if os.WriteFile(tmp, b, 0644) == nil {
		os.Rename(tmp, filepath.Join(m.upper, whiteoutFile))
	}
}

// ---- resolution ---------------------------------------------------------------

type resolved struct {
	upper string // real path in the upper dir
	entry *catEntry
	dir   bool
	st    syscall.Stat_t
}

func (m *mountFS) resolve(rel string) (*resolved, syscall.Errno) {
	if rel == "" {
		r := &resolved{upper: m.upper, dir: true}
		if err := syscall.Stat(m.upper, &r.st); err != nil {
			return nil, fs.ToErrno(err)
		}
		return r, 0
	}
	if strings.HasPrefix(path.Base(rel), ".hfd-") && !strings.Contains(rel, "/") {
		return nil, syscall.ENOENT
	}
	up := filepath.Join(m.upper, filepath.FromSlash(rel))
	r := &resolved{}
	if err := syscall.Stat(up, &r.st); err == nil { // follows upper symlinks
		r.upper = up
		r.dir = r.st.Mode&syscall.S_IFMT == syscall.S_IFDIR
		return r, 0
	}
	if m.whited(rel) {
		return nil, syscall.ENOENT
	}
	cat := m.cat.Load()
	if e := cat.files[rel]; e != nil {
		r.entry = e
		if e.Real != "" {
			if err := syscall.Stat(e.Real, &r.st); err != nil {
				return nil, syscall.ENOENT
			}
			r.st.Mode = syscall.S_IFREG | 0644
		} else {
			t := e.MTime.Unix()
			r.st = syscall.Stat_t{Mode: syscall.S_IFREG | 0644, Size: int64(len(e.Data)), Nlink: 1}
			r.st.Mtim.Sec, r.st.Ctim.Sec, r.st.Atim.Sec = t, t, t
		}
		r.st.Uid, r.st.Gid = m.uid, m.gid
		return r, 0
	}
	if _, ok := cat.dirs[rel]; ok {
		r.dir = true
		syscall.Stat(m.upper, &r.st)
		r.st.Mode = syscall.S_IFDIR | 0777
		r.st.Nlink = 2
		return r, 0
	}
	return nil, syscall.ENOENT
}

func inoFor(rel string, dir bool) uint64 {
	h := fnv.New64a()
	h.Write([]byte(rel))
	if dir {
		h.Write([]byte{0})
	}
	return (h.Sum64() &^ (1 << 63)) | 2
}

// ---- nodes ----------------------------------------------------------------------

type mnode struct {
	fs.Inode
	m *mountFS
}

var (
	_ = (fs.NodeLookuper)((*mnode)(nil))
	_ = (fs.NodeGetattrer)((*mnode)(nil))
	_ = (fs.NodeSetattrer)((*mnode)(nil))
	_ = (fs.NodeReaddirer)((*mnode)(nil))
	_ = (fs.NodeOpener)((*mnode)(nil))
	_ = (fs.NodeCreater)((*mnode)(nil))
	_ = (fs.NodeMkdirer)((*mnode)(nil))
	_ = (fs.NodeUnlinker)((*mnode)(nil))
	_ = (fs.NodeRmdirer)((*mnode)(nil))
	_ = (fs.NodeRenamer)((*mnode)(nil))
	_ = (fs.NodeStatfser)((*mnode)(nil))
)

func (n *mnode) rel() string {
	if n.IsRoot() {
		return ""
	}
	return n.Path(n.Root())
}

func (n *mnode) child(name string) string { return path.Join(n.rel(), name) }

func (n *mnode) fill(rel string, r *resolved, a *fuse.Attr) {
	a.FromStat(&r.st)
	a.Ino = inoFor(rel, r.dir)
	if r.dir {
		a.Mode = syscall.S_IFDIR | (a.Mode & 07777)
	}
}

func (n *mnode) newChild(ctx context.Context, rel string, r *resolved, out *fuse.EntryOut) *fs.Inode {
	n.fill(rel, r, &out.Attr)
	mode := uint32(syscall.S_IFREG)
	if r.dir {
		mode = syscall.S_IFDIR
	}
	return n.NewInode(ctx, &mnode{m: n.m}, fs.StableAttr{Mode: mode, Ino: inoFor(rel, r.dir)})
}

func (n *mnode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := n.child(name)
	r, errno := n.m.resolve(rel)
	if errno != 0 {
		return nil, errno
	}
	return n.newChild(ctx, rel, r, out), 0
}

func (n *mnode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if fg, ok := f.(fs.FileGetattrer); ok && f != nil {
		if _, mem := f.(*memFile); !mem {
			errno := fg.Getattr(ctx, out)
			out.Ino = inoFor(n.rel(), false)
			return errno
		}
	}
	rel := n.rel()
	r, errno := n.m.resolve(rel)
	if errno != 0 {
		return errno
	}
	n.fill(rel, r, &out.Attr)
	return 0
}

func (n *mnode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	rel := n.rel()
	r, errno := n.m.resolve(rel)
	if errno != 0 {
		return errno
	}
	if r.upper != "" {
		if fs2, ok := f.(fs.FileSetattrer); ok && f != nil {
			if errno := fs2.Setattr(ctx, in, out); errno != 0 {
				return errno
			}
		} else {
			if sz, ok := in.GetSize(); ok {
				if err := os.Truncate(r.upper, int64(sz)); err != nil {
					return fs.ToErrno(err)
				}
			}
			if md, ok := in.GetMode(); ok {
				os.Chmod(r.upper, os.FileMode(md&07777))
			}
			mt, mok := in.GetMTime()
			at, aok := in.GetATime()
			if mok || aok {
				if !mok {
					mt = time.Unix(r.st.Mtim.Unix())
				}
				if !aok {
					at = time.Unix(r.st.Atim.Unix())
				}
				os.Chtimes(r.upper, at, mt)
			}
		}
		r, errno = n.m.resolve(rel)
		if errno != 0 {
			return errno
		}
	}
	// Store files are read-only through the views; attribute changes on them
	// are accepted and ignored so tools that touch files do not fail.
	n.fill(rel, r, &out.Attr)
	return 0
}

func (n *mnode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	rel := n.rel()
	names := map[string]uint32{}
	if ents, err := os.ReadDir(filepath.Join(n.m.upper, filepath.FromSlash(rel))); err == nil {
		for _, e := range ents {
			if rel == "" && strings.HasPrefix(e.Name(), ".hfd-") {
				continue
			}
			p := filepath.Join(n.m.upper, filepath.FromSlash(rel), e.Name())
			fi, err := os.Stat(p)
			if err != nil {
				continue // dangling link
			}
			if fi.IsDir() {
				names[e.Name()] = syscall.S_IFDIR
			} else {
				names[e.Name()] = syscall.S_IFREG
			}
		}
	}
	cat := n.m.cat.Load()
	for k := range cat.dirs[rel] {
		if _, ok := names[k]; ok {
			continue
		}
		c := path.Join(rel, k)
		if n.m.whited(c) {
			continue
		}
		if _, isDir := cat.dirs[c]; isDir {
			names[k] = syscall.S_IFDIR
		} else {
			names[k] = syscall.S_IFREG
		}
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]fuse.DirEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, fuse.DirEntry{Name: k, Mode: names[k], Ino: inoFor(path.Join(rel, k), names[k] == syscall.S_IFDIR)})
	}
	return fs.NewListDirStream(out), 0
}

func isWrite(flags uint32) bool {
	return flags&syscall.O_ACCMODE != syscall.O_RDONLY || flags&syscall.O_TRUNC != 0
}

func (n *mnode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	rel := n.rel()
	r, errno := n.m.resolve(rel)
	if errno != 0 {
		return nil, 0, errno
	}
	flags &^= syscall.O_CREAT | syscall.O_EXCL
	switch {
	case r.upper != "":
		fd, err := syscall.Open(r.upper, int(flags), 0)
		if err != nil {
			return nil, 0, fs.ToErrno(err)
		}
		lf := fs.NewLoopbackFile(fd).(*fs.LoopbackFile)
		if isWrite(flags) {
			n.m.openedForWrite(r.upper)
			return &writeFile{LoopbackFile: lf, m: n.m, path: r.upper}, 0, 0
		}
		return lf, 0, 0
	case r.entry != nil && r.entry.Real != "":
		if isWrite(flags) {
			return nil, 0, syscall.EROFS
		}
		fd, err := syscall.Open(r.entry.Real, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, 0, fs.ToErrno(err)
		}
		return fs.NewLoopbackFile(fd), fuse.FOPEN_KEEP_CACHE, 0
	case r.entry != nil:
		if isWrite(flags) {
			return nil, 0, syscall.EROFS
		}
		return &memFile{data: r.entry.Data}, fuse.FOPEN_DIRECT_IO, 0
	}
	return nil, 0, syscall.EISDIR
}

// upperDirFor makes sure the parent of rel exists in the upper dir.
func (m *mountFS) upperPath(rel string) (string, syscall.Errno) {
	p := filepath.Join(m.upper, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return "", fs.ToErrno(err)
	}
	return p, 0
}

func (n *mnode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	rel := n.child(name)
	if rel == name {
		return nil, nil, 0, syscall.EPERM // views only at the top level
	}
	up, errno := n.m.upperPath(rel)
	if errno != 0 {
		return nil, nil, 0, errno
	}
	fd, err := syscall.Open(up, int(flags)|syscall.O_CREAT, mode)
	if err != nil {
		return nil, nil, 0, fs.ToErrno(err)
	}
	n.m.setWhiteout(rel, false)
	r, errno := n.m.resolve(rel)
	if errno != 0 {
		syscall.Close(fd)
		return nil, nil, 0, errno
	}
	n.m.openedForWrite(up)
	lf := fs.NewLoopbackFile(fd).(*fs.LoopbackFile)
	return n.newChild(ctx, rel, r, out), &writeFile{LoopbackFile: lf, m: n.m, path: up}, 0, 0
}

func (n *mnode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := n.child(name)
	if rel == name {
		return nil, syscall.EPERM
	}
	if r, errno := n.m.resolve(rel); errno == 0 && r.dir && r.upper != "" {
		return nil, syscall.EEXIST
	}
	up, errno := n.m.upperPath(rel)
	if errno != 0 {
		return nil, errno
	}
	if err := os.Mkdir(up, os.FileMode(mode&07777)|0700); err != nil && !os.IsExist(err) {
		return nil, fs.ToErrno(err)
	}
	n.m.setWhiteout(rel, false)
	r, errno := n.m.resolve(rel)
	if errno != 0 {
		return nil, errno
	}
	return n.newChild(ctx, rel, r, out), 0
}

func (m *mountFS) remove(rel string, dir bool) syscall.Errno {
	if rel == "" || !strings.Contains(rel, "/") {
		return syscall.EPERM
	}
	up := filepath.Join(m.upper, filepath.FromSlash(rel))
	removed := false
	if _, err := os.Lstat(up); err == nil {
		var err error
		if dir {
			err = syscall.Rmdir(up)
		} else {
			err = syscall.Unlink(up)
		}
		if err != nil {
			return fs.ToErrno(err)
		}
		removed = true
	}
	cat := m.cat.Load()
	_, inCat := cat.files[rel]
	if dir {
		_, inCat = cat.dirs[rel]
	}
	if inCat {
		// Ollama prunes and re-creates blobs freely; hiding a store blob
		// would break every manifest that shares it.
		if !strings.HasPrefix(rel, "ollama/blobs/") {
			m.setWhiteout(rel, true)
		}
		return 0
	}
	if !removed {
		return syscall.ENOENT
	}
	return 0
}

func (n *mnode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.m.remove(n.child(name), false)
}

func (n *mnode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.m.remove(n.child(name), true)
}

func (n *mnode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	src := n.child(name)
	np, ok := newParent.(*mnode)
	if !ok {
		return syscall.EXDEV
	}
	dst := np.child(newName)
	if !strings.Contains(src, "/") || !strings.Contains(dst, "/") {
		return syscall.EPERM
	}
	sup := filepath.Join(n.m.upper, filepath.FromSlash(src))
	if _, err := os.Lstat(sup); err != nil {
		return syscall.EPERM // store files are renamed in the store, not in a view
	}
	dup, errno := n.m.upperPath(dst)
	if errno != 0 {
		return errno
	}
	if flags&1 != 0 { // RENAME_NOREPLACE
		if _, e := n.m.resolve(dst); e == 0 {
			return syscall.EEXIST
		}
	}
	if err := os.Rename(sup, dup); err != nil {
		return fs.ToErrno(err)
	}
	n.m.setWhiteout(dst, false)
	if fi, err := os.Lstat(dup); err == nil && fi.Mode().IsRegular() && ingestCandidate(dup, fi) {
		n.m.pmu.Lock()
		n.m.pending[dup] = time.Now()
		n.m.pmu.Unlock()
		time.AfterFunc(n.m.opts.Watch.Settle, func() { n.m.tryIngest(dup) })
	}
	return 0
}

func (n *mnode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	var s syscall.Statfs_t
	if err := syscall.Statfs(n.m.upper, &s); err != nil {
		return fs.ToErrno(err)
	}
	out.FromStatfsT(&s)
	return 0
}

// ---- file handles -----------------------------------------------------------------

type writeFile struct {
	*fs.LoopbackFile
	m    *mountFS
	path string
	once sync.Once
}

func (w *writeFile) Release(ctx context.Context) syscall.Errno {
	errno := w.LoopbackFile.Release(ctx)
	w.once.Do(func() { w.m.closedWrite(w.path) })
	return errno
}

type memFile struct{ data []byte }

var _ = (fs.FileReader)((*memFile)(nil))

func (f *memFile) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(f.data)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(f.data)) {
		end = int64(len(f.data))
	}
	return fuse.ReadResultData(f.data[off:end]), 0
}
