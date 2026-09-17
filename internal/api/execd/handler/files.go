package handler

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/hostel/internal/api/execd/view"
)

func (s *Handler) execdRemoveDirectories(c *gin.Context) {
	_, ops, finish := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finish()
	paths := c.QueryArray("path")
	if len(paths) == 0 {
		badRequest(c, "missing path")
		return
	}
	for _, p := range paths {
		if p == "" || filepath.Clean(p) == "/" || filepath.Clean(p) == "." {
			badRequest(c, "non-root directory path required")
			return
		}
		if _, err := ops.Resolve(p); err != nil {
			badRequest(c, err.Error())
			return
		}
	}
	for _, p := range paths {
		if err := ops.RemoveDir(p); err != nil {
			runtimeError(c, err.Error())
			return
		}
	}
	c.Status(http.StatusOK)
}

func (s *Handler) execdDownload(c *gin.Context) {

	_, ops, finish := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finish()
	p := c.Query("path")
	if p == "" {
		badRequest(c, "missing path")
		return
	}
	reader := s.fileReader(ops)
	if c.Query("offset") != "" || c.Query("limit") != "" {
		if c.GetHeader("Range") != "" {
			badRequest(c, "Range and line parameters are mutually exclusive")
			return
		}
		offset, limit := 1, 0
		for name, dst := range map[string]*int{"offset": &offset, "limit": &limit} {
			if raw, ok := c.GetQuery(name); ok {
				n, err := strconv.Atoi(raw)
				if err != nil || n < 1 {
					badRequest(c, name+" must be a positive integer")
					return
				}
				*dst = n
			}
		}
		data, err := reader.ReadLines(p, offset-1, limit)
		if err != nil {
			downloadErr(c, err)
			return
		}
		c.Data(200, "text/plain; charset=utf-8", []byte(data))
		return
	}
	file, err := reader.Open(p)
	if err != nil {
		downloadErr(c, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		downloadErr(c, err)
		return
	}
	if !info.Mode().IsRegular() {
		badRequest(c, "download requires a regular file")
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(p)}))
	http.ServeContent(c.Writer, c.Request, filepath.Base(p), info.ModTime(), file)
}

func (s *Handler) execdUpload(c *gin.Context) {
	_, ops, finish := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finish()
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		badRequest(c, "invalid multipart upload")
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	form := c.Request.MultipartForm
	files := form.File["file"]
	metadata := form.Value["metadata"]
	// OpenSandbox SDKs send metadata as a JSON file part, not a form value.
	for _, part := range form.File["metadata"] {
		f, err := part.Open()
		if err != nil {
			runtimeError(c, err.Error())
			return
		}
		raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if err != nil {
			badRequest(c, "invalid metadata")
			return
		}
		metadata = append(metadata, string(raw))
	}
	if len(files) == 0 || len(files) != len(metadata) {
		badRequest(c, "file and metadata parts must be paired")
		return
	}
	for i, part := range files {
		var meta struct {
			Path string `json:"path"`
			view.Permission
		}
		if err := json.Unmarshal([]byte(metadata[i]), &meta); err != nil || meta.Path == "" {
			badRequest(c, "invalid metadata path")
			return
		}
		f, err := part.Open()
		if err != nil {
			runtimeError(c, err.Error())
			return
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			runtimeError(c, err.Error())
			return
		}
		perm, err := meta.Permission.Native()
		if err != nil {
			badRequest(c, err.Error())
			return
		}
		if err := ops.ValidatePermission(meta.Path, perm); err != nil {
			badRequest(c, err.Error())
			return
		}
		if err := ops.Write(meta.Path, data, 0); err != nil {
			runtimeError(c, err.Error())
			return
		}
		if meta.Permission.Mode != 0 || meta.Permission.Owner != "" || meta.Permission.Group != "" {
			if perm.Mode == 0 {
				info, err := ops.Stat(meta.Path)
				if err != nil {
					downloadErr(c, err)
					return
				}
				perm.Mode = info.Mode
			}
			if err := ops.Chmod(meta.Path, perm); err != nil {
				if os.IsNotExist(err) {
					downloadErr(c, err)
				} else {
					runtimeError(c, err.Error())
				}
				return
			}
		}
	}
	c.Status(http.StatusOK)
}
