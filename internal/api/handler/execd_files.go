package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	apiview "github.com/qiankunli/hostel/internal/api/view"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
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
	// The native API's offset/limit are line based; Execd defines byte offsets.
	// Reject extensions we do not implement instead of returning the wrong slice.
	if c.Query("offset") != "" || c.Query("limit") != "" || c.Query("line_start") != "" || c.Query("line_end") != "" {
		respondError(c, 400, apiview.ErrNotSupported, "partial download query is not supported")
		return
	}
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
	data, err := s.fileReader(ops).Read(p)
	if err != nil {
		downloadErr(c, err)
		return
	}
	c.Header("Content-Length", strconv.Itoa(len(data)))
	c.Data(http.StatusOK, "application/octet-stream", data)
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
			bedfs.Permission
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
		if err := ops.Write(meta.Path, data, 0); err != nil {
			runtimeError(c, err.Error())
			return
		}
		if meta.Permission.Mode != 0 || meta.Permission.Owner != "" || meta.Permission.Group != "" {
			if err := ops.Chmod(meta.Path, meta.Permission); err != nil {
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
