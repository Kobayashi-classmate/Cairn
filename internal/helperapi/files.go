// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"regexp"
	"strings"
)

// File actions. Every request names a root (one of FileRoots) and a path
// relative to it; the helper resolves it inside that root only (os.Root),
// so no request can reach a file outside it.
const (
	ActionFileRoots     = "file.roots"     // synchronous
	ActionFileList      = "file.list"      // synchronous
	ActionFileStat      = "file.stat"      // synchronous
	ActionFileRead      = "file.read"      // synchronous: a text file for the editor
	ActionFileWrite     = "file.write"     // synchronous
	ActionFileOpen      = "file.open"      // synchronous, answers with the open file (CallFD)
	ActionFileMkdir     = "file.mkdir"     // synchronous
	ActionFileTouch     = "file.touch"     // synchronous: a new empty file
	ActionFileRename    = "file.rename"    // synchronous
	ActionFileSearch    = "file.search"    // synchronous
	ActionFileCopy      = "file.copy"      // operation
	ActionFileMove      = "file.move"      // operation
	ActionFileDelete    = "file.delete"    // operation: to the trash, or for good
	ActionFileChmod     = "file.chmod"     // operation
	ActionFileChown     = "file.chown"     // operation
	ActionFileCompress  = "file.compress"  // operation
	ActionFileExtract   = "file.extract"   // operation
	ActionFilePlace     = "file.place"     // operation: an upload from the panel's directory
	ActionTrashList     = "file.trash"     // synchronous
	ActionTrashRestore  = "file.restore"   // operation
	ActionTrashPurge    = "file.purge"     // operation
	ActionAccessSet     = "access.set"     // synchronous: full-disk access, root terminal
	ActionTermUsers     = "term.users"     // synchronous
	ActionTermOpen      = "term.open"      // synchronous, answers with the terminal's master side
	ActionFileArchiving = "file.archivers" // synchronous: which archive tools exist
)

// File error codes.
const (
	CodeFileRootUnavailable = "file.root_unavailable"
	CodeFileNotFound        = "file.not_found"
	CodeFileExists          = "file.exists"
	CodeFileIsDir           = "file.is_dir"
	CodeFileNotDir          = "file.not_dir"
	CodeFileBinary          = "file.binary"
	CodeFileTooLarge        = "file.too_large"
	CodeFileChanged         = "file.changed" // changed since it was read
	CodeFileIsLink          = "file.is_link"
	CodeFileCrossDevice     = "file.cross_device" // the trash is on another file system
	CodeFileProtected       = "file.protected"
	CodeFileBadArchive      = "file.bad_archive"
	CodeFileToolMissing     = "file.tool_missing"
	CodeFileNoSpace         = "file.no_space"
	// CodeFileUnpackFailed: a 7z/RAR archive could not be prepared for
	// unar (params: reason = account | scratch | copy).
	CodeFileUnpackFailed = "file.unpack_failed"
	// CodeFileSystemd: 7z and RAR archives are unpacked in a transient
	// systemd unit, and this system does not run systemd.
	CodeFileSystemd        = "file.systemd_required"
	CodeFileUserUnknown    = "file.user_unknown"
	CodeFileIntoItself     = "file.into_itself" // a directory copied or moved into itself
	CodeAccessLocked       = "access.locked"    // the server's configuration forbids it
	CodeTermUserNotAllowed = "term.user_not_allowed"
	CodeTermRootDisabled   = "term.root_disabled"
	CodeTermTooMany        = "term.too_many"
)

// File roots.
const (
	RootSites   = "sites"   // /srv/www
	RootBackups = "backups" // the panel's backups
	RootSystem  = "system"  // /, with full-disk access only
)

// Limits.
const (
	FileListMax     = 5000
	FileReadMax     = 4 << 20
	FileSearchMax   = 500
	FileBatchMax    = 1000
	FileSearchBytes = 1 << 20 // files searched for content are at most this big
)

// Archive formats the helper writes (and reads; it also reads .tar and,
// with a system tool, .7z and .rar).
var ArchiveFormats = []string{"zip", "tar.gz", "tar.zst", "tar.xz"}

func validRoot(r string) bool { return r == RootSites || r == RootBackups || r == RootSystem }

// ValidFilePath reports whether p is a path relative to a root: "" is the
// root itself; otherwise slash-separated names, none of them "." or "..".
func ValidFilePath(p string) bool {
	if p == "" {
		return true
	}
	if len(p) > 4096 || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.ContainsRune(p, 0) {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if !ValidFileName(c) {
			return false
		}
	}
	return true
}

// ValidFileName reports whether s can name a directory entry.
func ValidFileName(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 255 && !strings.ContainsAny(s, "/\x00")
}

// FileRef names a file or directory.
type FileRef struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

// Validate implements the helper's request validation hook.
func (r FileRef) Validate() error {
	if !validRoot(r.Root) {
		return invalid("root", "oneof")
	}
	if !ValidFilePath(r.Path) {
		return invalid("path", "file_path")
	}
	return nil
}

// FileRootRef names a root.
type FileRootRef struct {
	Root string `json:"root"`
}

// Validate implements the helper's request validation hook.
func (r FileRootRef) Validate() error {
	if !validRoot(r.Root) {
		return invalid("root", "oneof")
	}
	return nil
}

func validPaths(root string, paths []string) error {
	if !validRoot(root) {
		return invalid("root", "oneof")
	}
	if len(paths) == 0 || len(paths) > FileBatchMax {
		return invalid("paths", "count")
	}
	for _, p := range paths {
		if p == "" || !ValidFilePath(p) {
			return invalid("paths", "file_path")
		}
	}
	return nil
}

// FileRoot is a root the file manager offers.
type FileRoot struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Available bool   `json:"available"`
}

// AccessSettings are the helper's own switches for root access.
type AccessSettings struct {
	FullDisk     bool `json:"fullDisk"`
	RootTerminal bool `json:"rootTerminal"`
	// Locked: the server's configuration ([security] root_access =
	// "never") keeps both off.
	Locked bool `json:"locked,omitempty"`
}

// Validate implements the helper's request validation hook.
func (AccessSettings) Validate() error { return nil }

// FileRoots is the result of file.roots.
type FileRoots struct {
	Roots    []FileRoot     `json:"roots"`
	Settings AccessSettings `json:"settings"`
}

// File entry types.
const (
	FileTypeFile = "file"
	FileTypeDir  = "dir"
	FileTypeLink = "link"
	// FileTypeOther: devices, sockets, pipes.
	FileTypeOther = "other"
)

// FileEntry describes a directory entry (links are not followed).
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"` // permission and special bits
	Owner   string `json:"owner"`
	Group   string `json:"group"`
	ModTime int64  `json:"modTime"`
	// Target is a link's target as written in the link.
	Target string `json:"target,omitempty"`
}

// FileListRequest lists a directory.
type FileListRequest struct {
	Root   string `json:"root"`
	Path   string `json:"path"`
	Hidden bool   `json:"hidden,omitempty"` // include dot files
}

// Validate implements the helper's request validation hook.
func (r FileListRequest) Validate() error { return FileRef{Root: r.Root, Path: r.Path}.Validate() }

// FileList is a directory's entries: directories first, then by name.
type FileList struct {
	Dir       FileEntry   `json:"dir"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated,omitempty"`
}

// FileContent is a text file for the editor. ETag identifies the version
// read (size and modification time).
type FileContent struct {
	Entry   FileEntry `json:"entry"`
	Content string    `json:"content"`
	ETag    string    `json:"etag"`
}

// FileWriteRequest saves a text file. ETag is the version it was read as:
// a file changed since fails (file.changed); "" creates a new file, which
// must not exist yet.
type FileWriteRequest struct {
	Root    string `json:"root"`
	Path    string `json:"path"`
	Content string `json:"content"`
	ETag    string `json:"etag,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r FileWriteRequest) Validate() error {
	if err := (FileRef{Root: r.Root, Path: r.Path}).Validate(); err != nil {
		return err
	}
	if r.Path == "" {
		return invalid("path", "file_path")
	}
	if len(r.Content) > FileReadMax {
		return invalid("content", "max")
	}
	return nil
}

// FileNewRequest creates Name in Dir.
type FileNewRequest struct {
	Root string `json:"root"`
	Dir  string `json:"dir"`
	Name string `json:"name"`
}

// Validate implements the helper's request validation hook.
func (r FileNewRequest) Validate() error {
	if err := (FileRef{Root: r.Root, Path: r.Dir}).Validate(); err != nil {
		return err
	}
	if !ValidFileName(r.Name) {
		return invalid("name", "file_name")
	}
	return nil
}

// FileRenameRequest renames an entry within its directory.
type FileRenameRequest struct {
	Root string `json:"root"`
	Path string `json:"path"`
	Name string `json:"name"`
}

// Validate implements the helper's request validation hook.
func (r FileRenameRequest) Validate() error {
	if err := validPaths(r.Root, []string{r.Path}); err != nil {
		return err
	}
	if !ValidFileName(r.Name) {
		return invalid("name", "file_name")
	}
	return nil
}

// FileTransferRequest copies or moves entries into a directory.
type FileTransferRequest struct {
	Root      string   `json:"root"`
	Paths     []string `json:"paths"`
	Dest      string   `json:"dest"`
	Overwrite bool     `json:"overwrite,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r FileTransferRequest) Validate() error {
	if err := validPaths(r.Root, r.Paths); err != nil {
		return err
	}
	if !ValidFilePath(r.Dest) {
		return invalid("dest", "file_path")
	}
	return nil
}

// FileDeleteRequest deletes entries: to the trash, or for good.
type FileDeleteRequest struct {
	Root      string   `json:"root"`
	Paths     []string `json:"paths"`
	Permanent bool     `json:"permanent,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r FileDeleteRequest) Validate() error { return validPaths(r.Root, r.Paths) }

// Mode bits the panel sets: permissions, setgid and sticky on directories.
// Setuid and setgid on files are never set through the panel.
const (
	FileModeMask = 0o777
	DirModeMask  = 0o3777
)

// FileChmodRequest changes permissions; with Recursive, directories get
// DirMode and files Mode below each path.
type FileChmodRequest struct {
	Root      string   `json:"root"`
	Paths     []string `json:"paths"`
	Mode      uint32   `json:"mode"`
	DirMode   uint32   `json:"dirMode,omitempty"`
	Recursive bool     `json:"recursive,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r FileChmodRequest) Validate() error {
	if err := validPaths(r.Root, r.Paths); err != nil {
		return err
	}
	if r.Mode&^DirModeMask != 0 || r.DirMode&^DirModeMask != 0 {
		return invalid("mode", "file_mode")
	}
	return nil
}

var posixName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}\$?$`)

// FileChownRequest changes owner and group ("" keeps one).
type FileChownRequest struct {
	Root      string   `json:"root"`
	Paths     []string `json:"paths"`
	User      string   `json:"user,omitempty"`
	Group     string   `json:"group,omitempty"`
	Recursive bool     `json:"recursive,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r FileChownRequest) Validate() error {
	if err := validPaths(r.Root, r.Paths); err != nil {
		return err
	}
	if (r.User == "" && r.Group == "") || (r.User != "" && !posixName.MatchString(r.User)) || (r.Group != "" && !posixName.MatchString(r.Group)) {
		return invalid("user", "posix_name")
	}
	return nil
}

// FileCompressRequest archives Names (entries of Dir) into Dir/Name.
type FileCompressRequest struct {
	Root   string   `json:"root"`
	Dir    string   `json:"dir"`
	Names  []string `json:"names"`
	Format string   `json:"format"`
	Name   string   `json:"name"`
}

// Validate implements the helper's request validation hook.
func (r FileCompressRequest) Validate() error {
	if err := (FileRef{Root: r.Root, Path: r.Dir}).Validate(); err != nil {
		return err
	}
	if len(r.Names) == 0 || len(r.Names) > FileBatchMax {
		return invalid("names", "count")
	}
	for _, n := range r.Names {
		if !ValidFileName(n) {
			return invalid("names", "file_name")
		}
	}
	if !containsString(ArchiveFormats, r.Format) {
		return invalid("format", "oneof")
	}
	if !ValidFileName(r.Name) || !strings.HasSuffix(r.Name, "."+r.Format) {
		return invalid("name", "archive_name")
	}
	return nil
}

// FileExtractRequest unpacks the archive at Path into Dest.
type FileExtractRequest struct {
	Root      string `json:"root"`
	Path      string `json:"path"`
	Dest      string `json:"dest"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r FileExtractRequest) Validate() error {
	if err := validPaths(r.Root, []string{r.Path}); err != nil {
		return err
	}
	if !ValidFilePath(r.Dest) {
		return invalid("dest", "file_path")
	}
	return nil
}

// FileSearchRequest finds entries below Path whose name contains Query
// or, with Content, text files that contain it.
type FileSearchRequest struct {
	Root          string `json:"root"`
	Path          string `json:"path"`
	Query         string `json:"query"`
	Content       bool   `json:"content,omitempty"`
	CaseSensitive bool   `json:"caseSensitive,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r FileSearchRequest) Validate() error {
	if err := (FileRef{Root: r.Root, Path: r.Path}).Validate(); err != nil {
		return err
	}
	if r.Query == "" || len(r.Query) > 256 || strings.ContainsAny(r.Query, "\x00\n") {
		return invalid("query", "search_query")
	}
	return nil
}

// FileMatch is a search hit: an entry, or a line of a file (Line > 0).
type FileMatch struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
}

// FileSearchResult lists hits; Truncated when the limits stopped it.
type FileSearchResult struct {
	Matches   []FileMatch `json:"matches"`
	Truncated bool        `json:"truncated,omitempty"`
}

var uploadID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidUploadID reports whether s names an upload of the panel.
func ValidUploadID(s string) bool { return uploadID.MatchString(s) }

// FilePlaceRequest moves a finished upload (the panel's uploads/<id>.part)
// to Dir/Name.
type FilePlaceRequest struct {
	Upload    string `json:"upload"`
	Root      string `json:"root"`
	Dir       string `json:"dir"`
	Name      string `json:"name"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r FilePlaceRequest) Validate() error {
	if !ValidUploadID(r.Upload) {
		return invalid("upload", "upload_id")
	}
	return FileNewRequest{Root: r.Root, Dir: r.Dir, Name: r.Name}.Validate()
}

// TrashEntry is an entry in a root's trash.
type TrashEntry struct {
	ID        string `json:"id"`
	Path      string `json:"path"` // where it was
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	DeletedAt int64  `json:"deletedAt"`
	User      string `json:"user,omitempty"`
}

// TrashList is the result of file.trash.
type TrashList struct {
	Entries []TrashEntry `json:"entries"`
}

var trashID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// TrashRequest restores trash entries.
type TrashRequest struct {
	Root      string   `json:"root"`
	IDs       []string `json:"ids"`
	Overwrite bool     `json:"overwrite,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r TrashRequest) Validate() error {
	if !validRoot(r.Root) {
		return invalid("root", "oneof")
	}
	if len(r.IDs) == 0 || len(r.IDs) > FileBatchMax {
		return invalid("ids", "count")
	}
	for _, id := range r.IDs {
		if !trashID.MatchString(id) {
			return invalid("ids", "trash_id")
		}
	}
	return nil
}

// TrashPurgeRequest deletes trash entries for good: the listed ones, or
// those deleted before OlderThan (Unix seconds).
type TrashPurgeRequest struct {
	Root      string   `json:"root"`
	IDs       []string `json:"ids,omitempty"`
	OlderThan int64    `json:"olderThan,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r TrashPurgeRequest) Validate() error {
	if len(r.IDs) == 0 {
		if !validRoot(r.Root) {
			return invalid("root", "oneof")
		}
		if r.OlderThan <= 0 {
			return invalid("olderThan", "required")
		}
		return nil
	}
	return TrashRequest{Root: r.Root, IDs: r.IDs}.Validate()
}

// FileOpResult reports what an operation did with each path.
type FileOpResult struct {
	Done   int      `json:"done"`
	Failed []string `json:"failed,omitempty"`
}

// Archivers says which archive formats this system can read beyond the
// built-in ones.
type Archivers struct {
	SevenZip bool `json:"sevenZip"`
	RAR      bool `json:"rar"`
}

// Terminal kinds.
const (
	TermUserSite = "site"
	TermUserUser = "user"
	TermUserRoot = "root"
)

// TermUser is someone a terminal can run as.
type TermUser struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Home string `json:"home"`
	Site string `json:"site,omitempty"`
}

// TermUsers is the result of term.users.
type TermUsers struct {
	Users        []TermUser `json:"users"`
	RootTerminal bool       `json:"rootTerminal"`
	// Locked: the server's configuration keeps root terminals off.
	Locked bool `json:"locked,omitempty"`
}

// FileSaved is the result of file.write: the entry and its new version.
type FileSaved struct {
	Entry FileEntry `json:"entry"`
	ETag  string    `json:"etag"`
}

var termUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// TermOpenRequest opens a terminal as User.
type TermOpenRequest struct {
	User string `json:"user"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// Validate implements the helper's request validation hook.
func (r TermOpenRequest) Validate() error {
	if !termUser.MatchString(r.User) {
		return invalid("user", "posix_name")
	}
	if r.Cols < 10 || r.Cols > 1000 || r.Rows < 2 || r.Rows > 500 {
		return invalid("size", "range")
	}
	return nil
}

// TermSession describes an opened terminal.
type TermSession struct {
	PID int `json:"pid,omitempty"`
	// Unit is the shell's systemd unit, where systemd runs.
	Unit  string `json:"unit,omitempty"`
	User  string `json:"user"`
	Home  string `json:"home"`
	Shell string `json:"shell"`
}
