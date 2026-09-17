package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	view "github.com/qiankunli/hostel/internal/api/execd/view"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

func (s *Handler) execdInfo(c *gin.Context) {
	_, fs, finish := s.opsOf(c)
	if fs == nil {
		return
	}
	defer finish()
	paths := c.QueryArray("path")
	if len(paths) == 0 {
		badRequest(c, "missing path")
		return
	}
	out := make(map[string]view.FileInfo, len(paths))
	for _, p := range paths {
		f, err := s.fileReader(fs).Stat(p)
		if err != nil {
			downloadErr(c, err)
			return
		}
		out[p] = view.File(f)
	}
	c.JSON(200, out)
}
func (s *Handler) execdList(c *gin.Context)   { s.execdListOrSearch(c, false) }
func (s *Handler) execdSearch(c *gin.Context) { s.execdListOrSearch(c, true) }
func (s *Handler) execdListOrSearch(c *gin.Context, search bool) {
	_, fs, finish := s.opsOf(c)
	if fs == nil {
		return
	}
	defer finish()
	p := c.Query("path")
	if p == "" {
		badRequest(c, "missing path")
		return
	}
	depth := 1
	if q, ok := c.GetQuery("depth"); ok {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			badRequest(c, "depth must be a nonnegative integer")
			return
		}
		depth = n
	}
	reader := s.fileReader(fs)
	var files []bedfs.FileInfo
	var err error
	if search {
		files, err = reader.Search(p, c.Query("pattern"))
	} else {
		f, statErr := reader.Stat(p)
		if statErr != nil {
			downloadErr(c, statErr)
			return
		}
		if f.Type != "directory" {
			badRequest(c, "path must name a real directory")
			return
		}
		if depth > 0 {
			files, err = reader.List(p, depth)
		}
	}
	if err != nil {
		downloadErr(c, err)
		return
	}
	out := make([]view.FileInfo, 0, len(files))
	for _, f := range files {
		out = append(out, view.File(f))
	}
	c.JSON(200, out)
}
func (s *Handler) execdPermissions(c *gin.Context) { s.execdSetPermissions(c, false) }
func (s *Handler) execdMkdir(c *gin.Context)       { s.execdSetPermissions(c, true) }
func (s *Handler) execdSetPermissions(c *gin.Context, create bool) {
	_, fs, finish := s.opsOf(c)
	if fs == nil {
		return
	}
	defer finish()
	var items map[string]view.Permission
	if err := c.ShouldBindJSON(&items); err != nil || len(items) == 0 {
		badRequest(c, "expected path-to-permission map")
		return
	}
	for p, raw := range items {
		perm, err := raw.Native()
		if err != nil {
			badRequest(c, err.Error())
			return
		}
		if err = fs.ValidatePermission(p, perm); err != nil {
			badRequest(c, err.Error())
			return
		}
	}
	for p, raw := range items {
		perm, _ := raw.Native()
		if create {
			if err := fs.MakeDir(p); err != nil {
				downloadErr(c, err)
				return
			}
		}
		if perm.Mode == 0 && perm.Owner == "" && perm.Group == "" {
			continue
		}
		if perm.Mode == 0 {
			info, err := fs.Stat(p)
			if err != nil {
				downloadErr(c, err)
				return
			}
			perm.Mode = info.Mode
		}
		if err := fs.Chmod(p, perm); err != nil {
			downloadErr(c, err)
			return
		}
	}
	c.Status(200)
}
