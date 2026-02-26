package fileops

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const (
	defaultMaxFileSize = 10 * 1024 * 1024 // 10MB
	binaryCheckBytes   = 512
)

var blockedSearchRoots = map[string]struct{}{
	"proc": {},
	"sys":  {},
	"dev":  {},
	"run":  {},
}

// Policy controls what file operations are allowed.
type Policy struct {
	AllowedPaths []string
	BlockedPaths []string
	MaxFileSize  int64
}

// SearchResult is returned by SearchFiles.
type SearchResult struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// FileInfo is returned by StatFile.
type FileInfo struct {
	Path    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	IsDir   bool
	Owner   string
	Group   string
}

// FileOps performs guarded file operations.
type FileOps struct {
	policy Policy
	logger *zap.Logger
}

// New creates a new FileOps instance.
func New(policy Policy, logger *zap.Logger) *FileOps {
	if policy.MaxFileSize <= 0 {
		policy.MaxFileSize = defaultMaxFileSize
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	return &FileOps{
		policy: policy,
		logger: logger,
	}
}

// ReadFile reads a file with enforcement.
func (f *FileOps) ReadFile(path string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		maxBytes = f.policy.MaxFileSize
	}
	if maxBytes > f.policy.MaxFileSize {
		maxBytes = f.policy.MaxFileSize
	}

	resolved, err := f.resolvePath(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory: %s", path)
	}
	if info.Size() > maxBytes {
		return "", fmt.Errorf("file too large: %d > %d", info.Size(), maxBytes)
	}

	file, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	defer file.Close()

	head := make([]byte, binaryCheckBytes)
	n, err := file.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	head = head[:n]
	if isBinary(head) {
		return "", fmt.Errorf("binary file detected: %s", path)
	}

	remaining := maxBytes - int64(n)
	if remaining < 0 {
		return "", fmt.Errorf("maxBytes too small")
	}

	rest, err := io.ReadAll(io.LimitReader(file, remaining))
	if err != nil {
		return "", err
	}

	f.logger.Debug("read file",
		zap.String("path", path),
		zap.Int("bytes", len(head)+len(rest)),
	)

	return string(append(head, rest...)), nil
}

// SearchFiles recursively searches for files under root matching pattern.
func (f *FileOps) SearchFiles(root string, pattern string, maxResults int) ([]SearchResult, error) {
	if maxResults < 0 {
		return nil, fmt.Errorf("maxResults must be >= 0")
	}

	resolvedRoot, err := f.resolvePath(root)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(resolvedRoot)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", resolvedRoot)
	}

	if shouldSkipSearchRoot(resolvedRoot) {
		return []SearchResult{}, nil
	}

	if _, err := filepath.Match(pattern, "x"); err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0)
	err = filepath.WalkDir(resolvedRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if shouldSkipSearchRoot(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if err := f.checkPolicy(path); err != nil {
			if d.IsDir() {
				f.logger.Warn("skipping blocked directory in search", zap.String("path", path), zap.Error(err))
				return filepath.SkipDir
			}
			f.logger.Warn("skipping blocked path in search", zap.String("path", path), zap.Error(err))
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		match, err := filepath.Match(pattern, d.Name())
		if err != nil {
			return err
		}
		if match {
			results = append(results, SearchResult{
				Path:    path,
				Size:    info.Size(),
				ModTime: info.ModTime(),
				IsDir:   d.IsDir(),
			})
			if maxResults > 0 && len(results) >= maxResults {
				return filepath.SkipAll
			}
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, filepath.SkipAll) {
			return results, nil
		}
		return nil, err
	}

	f.logger.Debug("search files",
		zap.String("root", root),
		zap.String("pattern", pattern),
		zap.Int("results", len(results)),
	)

	return results, nil
}

// StatFile returns file metadata.
func (f *FileOps) StatFile(path string) (*FileInfo, error) {
	resolved, err := f.resolvePath(path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}

	sysInfo, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("failed to read file owner metadata")
	}

	owner := lookupUserName(sysInfo.Uid)
	group := lookupGroupName(sysInfo.Gid)

	f.logger.Debug("stat file", zap.String("path", path))

	return &FileInfo{
		Path:    resolved,
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
		Owner:   owner,
		Group:   group,
	}, nil
}

// ReadLines reads a range of lines from a file.
func (f *FileOps) ReadLines(path string, start, count int) ([]string, error) {
	if start <= 0 {
		return nil, fmt.Errorf("start must be >= 1")
	}
	if count <= 0 {
		return []string{}, nil
	}

	resolved, err := f.resolvePath(path)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	lines := make([]string, 0, count)
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo < start {
			continue
		}
		if len(lines) >= count {
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	f.logger.Debug("read lines", zap.String("path", path), zap.Int("returned", len(lines)))
	return lines, nil
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0
}

func (f *FileOps) resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)

	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}

	resolved := abs
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		resolved = filepath.Clean(resolved)
	}

	if err := f.checkPolicy(resolved); err != nil {
		return "", err
	}

	return resolved, nil
}

func (f *FileOps) checkPolicy(path string) error {
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	cleanPath = filepath.Clean(cleanPath)

	for _, blocked := range f.policy.BlockedPaths {
		blockedPath, err := filepath.Abs(blocked)
		if err != nil {
			f.logger.Warn("invalid blocked path in policy", zap.String("path", blocked), zap.Error(err))
			continue
		}
		blockedPath = filepath.Clean(blockedPath)
		if pathIsWithin(cleanPath, blockedPath) {
			return fmt.Errorf("path blocked by policy: %s", path)
		}
	}

	if len(f.policy.AllowedPaths) == 0 {
		return nil
	}

	for _, allowed := range f.policy.AllowedPaths {
		allowedPath, err := filepath.Abs(allowed)
		if err != nil {
			f.logger.Warn("invalid allowed path in policy", zap.String("path", allowed), zap.Error(err))
			continue
		}
		allowedPath = filepath.Clean(allowedPath)
		if pathIsWithin(cleanPath, allowedPath) {
			return nil
		}
	}

	return fmt.Errorf("path not allowed by policy: %s", path)
}

func pathIsWithin(path, scope string) bool {
	rel, err := filepath.Rel(scope, path)
	if err != nil {
		return false
	}

	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func shouldSkipSearchRoot(path string) bool {
	base := filepath.Base(path)
	_, skip := blockedSearchRoots[base]
	return skip
}

func lookupUserName(uid uint32) string {
	owner := fmt.Sprint(uid)
	if userInfo, err := user.LookupId(owner); err == nil {
		owner = userInfo.Username
	}
	return owner
}

func lookupGroupName(gid uint32) string {
	group := fmt.Sprint(gid)
	if groupInfo, err := user.LookupGroupId(group); err == nil {
		group = groupInfo.Name
	}
	return group
}
