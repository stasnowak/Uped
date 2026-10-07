package store

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stasnowak/Uped/internal/names"
)

// Entry is one item in a folder listing.
type Entry struct {
	Name      string     `json:"name"`
	Type      string     `json:"type"`  // "file" or "dir"
	Size      int64      `json:"size"`  // bytes; for folders, all files inside
	Items     int        `json:"items"` // folders: number of files inside, recursively
	Modified  time.Time  `json:"modified"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"` // nil when expiry is off or a folder is empty
	Device    string     `json:"device,omitempty"`
	Preview   string     `json:"preview,omitempty"` // start of small .txt files
}

// Listing is the content of one folder plus uploads heading into it.
type Listing struct {
	Path    string       `json:"path"`
	Entries []Entry      `json:"entries"` // newest first
	Uploads []UploadInfo `json:"uploads"` // active uploads into this folder or below it
	Free    int64        `json:"free"`    // free bytes, -1 if unknown
}

// List returns the folder dir, newest entries first. Symlinks and other
// special files are not shown.
func (s *Store) List(dir []string) (Listing, error) {
	if err := checkSegs(dir); err != nil {
		return Listing{}, err
	}
	p := filesPath(dir)
	fi, err := s.root.Stat(p)
	if err != nil {
		return Listing{}, mapErr(err)
	}
	if !fi.IsDir() {
		return Listing{}, ErrNotDir
	}
	dirEntries, err := s.readDir(p)
	if err != nil {
		return Listing{}, mapErr(err)
	}

	entries := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		child := path.Join(p, de.Name())
		switch {
		case de.IsDir():
			e := Entry{Name: de.Name(), Type: "dir"}
			if info, err := de.Info(); err == nil {
				e.Modified = info.ModTime()
			}
			s.dirStats(child, &e)
			entries = append(entries, e)
		case de.Type().IsRegular():
			info, err := de.Info()
			if err != nil {
				continue
			}
			e := Entry{Name: de.Name(), Type: "file", Size: info.Size(), Modified: info.ModTime(), ExpiresAt: s.expiry(info.ModTime())}
			if strings.EqualFold(path.Ext(e.Name), ".txt") && e.Size <= previewMaxFile {
				e.Preview = s.preview(child)
			}
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].Modified.Equal(entries[j].Modified) {
			return entries[i].Modified.After(entries[j].Modified)
		}
		return entries[i].Name < entries[j].Name
	})

	l := Listing{Path: names.Join(dir), Entries: entries, Uploads: []UploadInfo{}, Free: -1}
	s.mu.Lock()
	for i := range l.Entries {
		if l.Entries[i].Type == "file" {
			l.Entries[i].Device = s.meta[names.Join(append(append([]string{}, dir...), l.Entries[i].Name))].Device
		}
	}
	for _, u := range s.uploads {
		if hasPrefixSegs(u.Dir, dir) {
			l.Uploads = append(l.Uploads, u.info(StateActive))
		}
	}
	s.mu.Unlock()
	sort.Slice(l.Uploads, func(i, j int) bool { return l.Uploads[i].Created.Before(l.Uploads[j].Created) })
	if free, err := s.opts.FreeSpace(); err == nil {
		l.Free = free
	}
	return l, nil
}

func (s *Store) expiry(mod time.Time) *time.Time {
	if s.opts.TTL <= 0 {
		return nil
	}
	t := mod.Add(s.opts.TTL)
	return &t
}

// dirStats fills a folder entry with totals over the files inside it.
func (s *Store) dirStats(p string, e *Entry) {
	var latest time.Time
	_ = fs.WalkDir(s.root.FS(), p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		e.Items++
		e.Size += info.Size()
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	if e.Items > 0 {
		e.Modified = latest
		e.ExpiresAt = s.expiry(latest)
	}
}

// preview returns the first previewRunes characters of a text file.
func (s *Store) preview(p string) string {
	f, err := s.root.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, previewRunes*utf8.UTFMax)
	n, _ := io.ReadFull(f, buf)
	if n == len(buf) {
		// The read may have cut the last character in half; drop it.
		i := n - 1
		for i > 0 && !utf8.RuneStart(buf[i]) {
			i--
		}
		if !utf8.FullRune(buf[i:n]) {
			n = i
		}
	}
	text := strings.ToValidUTF8(string(buf[:n]), string(utf8.RuneError))
	if utf8.RuneCountInString(text) > previewRunes {
		text = string([]rune(text)[:previewRunes])
	}
	return text
}

// Delete removes a file or a folder with everything in it. Active uploads
// into a deleted folder are aborted. Deleting the root is not allowed.
func (s *Store) Delete(rel []string) error {
	if len(rel) == 0 {
		return ErrBadPath
	}
	if err := checkSegs(rel); err != nil {
		return err
	}
	p := filesPath(rel)
	fi, err := s.root.Lstat(p)
	if err != nil {
		return mapErr(err)
	}

	s.mu.Lock()
	if fi.IsDir() {
		for _, u := range s.uploads {
			if hasPrefixSegs(u.Dir, rel) {
				s.abortLocked(u)
			}
		}
	}
	s.mu.Unlock()

	if fi.IsDir() {
		err = s.removeAll(p)
	} else {
		err = s.root.Remove(p)
	}

	s.mu.Lock()
	s.dropMetaLocked(names.Join(rel))
	s.mu.Unlock()
	s.emitChange(rel[:len(rel)-1])
	return mapErr(err)
}

// PutText saves a pasted text snippet into folder dir under a name derived
// from its first line, and returns its path.
func (s *Store) PutText(dir []string, text, device string) (string, error) {
	if len(text) > MaxTextBytes {
		return "", ErrTooLarge
	}
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(device) > 200 {
		return "", ErrInvalid
	}
	if err := checkSegs(dir); err != nil {
		return "", err
	}
	name := names.SnippetName(text, s.opts.Now())

	s.mu.Lock()
	short := s.insufficientLocked(int64(len(text)))
	s.mu.Unlock()
	if short {
		return "", ErrInsufficientSpace
	}

	tmp := newID() + ".txt.tmp"
	f, err := s.root.OpenFile(partsDir+"/"+tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return "", err
	}
	_, err = io.WriteString(f, text)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = s.root.Remove(partsDir + "/" + tmp)
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rel, err := s.placeLocked(dir, name, tmp)
	if err != nil {
		_ = s.root.Remove(partsDir + "/" + tmp)
		return "", err
	}
	s.meta[rel] = metaEntry{Device: device}
	s.markMetaDirtyLocked()
	return rel, nil
}

// placeLocked moves .parts/tmp to a deduplicated name in dir and emits the
// change.
func (s *Store) placeLocked(dir []string, name, tmp string) (string, error) {
	resolved, err := s.resolveDirLocked(dir)
	if err != nil {
		return "", err
	}
	taken, err := s.takenLocked(resolved, "")
	if err != nil {
		return "", err
	}
	name = names.Dedupe(func(n string) bool { return taken[strings.ToLower(n)] }, name)
	if err := s.moveIntoLocked(resolved, tmp, name); err != nil {
		return "", mapErr(err)
	}
	s.emitChange(resolved)
	return names.Join(append(append([]string{}, resolved...), name)), nil
}

// OpenFile opens a finished file for download. The caller closes it.
func (s *Store) OpenFile(rel []string) (*os.File, fs.FileInfo, error) {
	if len(rel) == 0 {
		return nil, nil, ErrIsDir
	}
	if err := checkSegs(rel); err != nil {
		return nil, nil, err
	}
	f, err := s.root.Open(filesPath(rel))
	if err != nil {
		return nil, nil, mapErr(err)
	}
	fi, err := f.Stat()
	switch {
	case err != nil:
		f.Close()
		return nil, nil, err
	case fi.IsDir():
		f.Close()
		return nil, nil, ErrIsDir
	case !fi.Mode().IsRegular():
		f.Close()
		return nil, nil, ErrNotFound
	}
	return f, fi, nil
}

// StatDir checks that dir is an existing folder, for callers that want to
// fail before they start streaming a zip.
func (s *Store) StatDir(dir []string) error {
	if err := checkSegs(dir); err != nil {
		return err
	}
	fi, err := s.root.Stat(filesPath(dir))
	if err != nil {
		return mapErr(err)
	}
	if !fi.IsDir() {
		return ErrNotDir
	}
	return nil
}

// WriteZip streams folder dir as a zip archive to w. Entries are stored
// uncompressed (photos and video do not shrink, and it keeps a small LXC
// fast); archive/zip writes data descriptors and zip64 records as needed, so
// nothing is buffered. A non-root folder's name prefixes every entry. flush,
// if not nil, runs after each file. Files that vanish mid-walk are skipped;
// a write error (the client went away) stops the walk and is returned.
func (s *Store) WriteZip(w io.Writer, dir []string, flush func()) error {
	if err := s.StatDir(dir); err != nil {
		return err
	}
	p := filesPath(dir)
	prefix := ""
	if len(dir) > 0 {
		prefix = dir[len(dir)-1] + "/"
	}
	fsys := s.root.FS()
	zw := zip.NewWriter(w)
	err := fs.WalkDir(fsys, p, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			if fp == p {
				return mapErr(err)
			}
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(fp, p), "/")
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel == "" && prefix == "" {
				return nil
			}
			name := prefix + rel
			if !strings.HasSuffix(name, "/") {
				name += "/"
			}
			hdr := &zip.FileHeader{Name: name, Method: zip.Store, Modified: info.ModTime()}
			hdr.SetMode(fs.ModeDir | 0o755)
			_, err := zw.CreateHeader(hdr)
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := fsys.Open(fp)
		if err != nil {
			return nil
		}
		defer f.Close()
		hdr := &zip.FileHeader{Name: prefix + rel, Method: zip.Store, Modified: info.ModTime()}
		hdr.SetMode(0o644)
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := io.Copy(fw, f); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return zw.Close()
}
