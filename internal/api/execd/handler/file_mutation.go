package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	apiview "github.com/qiankunli/hostel/internal/api/execd/view"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

func (s *Handler) filesDelete(c *gin.Context) {
	_, ops, finishOperation := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finishOperation()
	paths := c.QueryArray("path")
	if len(paths) == 0 {
		respondError(c, http.StatusBadRequest, apiview.ErrMissingQuery, "missing query parameter 'path'")
		return
	}
	if err := ops.Remove(paths); err != nil {
		runtimeError(c, err.Error())
		return
	}
	c.Status(http.StatusOK)
}

func (s *Handler) filesRename(c *gin.Context) {
	_, ops, finishOperation := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finishOperation()
	var items []apiview.MoveItem
	if err := c.ShouldBindJSON(&items); err != nil {
		badRequest(c, err.Error())
		return
	}
	for _, it := range items {
		if err := ops.Rename(it.Src, it.Dest); err != nil {
			runtimeError(c, err.Error())
			return
		}
	}
	c.Status(http.StatusOK)
}

func (s *Handler) filesReplace(c *gin.Context) {
	_, ops, finishOperation := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finishOperation()
	var m map[string]apiview.ReplaceItem
	if err := c.ShouldBindJSON(&m); err != nil {
		badRequest(c, err.Error())
		return
	}
	out := make(map[string]apiview.ReplaceResult, len(m))
	for p, item := range m {
		res, err := ops.Replace(p, bedfs.ReplaceItem{Old: item.Old, New: item.New})
		if err != nil {
			runtimeError(c, err.Error())
			return
		}
		out[p] = apiview.ReplaceResult{ReplacedCount: res.ReplacedCount}
	}
	c.JSON(http.StatusOK, out)
}
