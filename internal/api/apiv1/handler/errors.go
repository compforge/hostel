// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	hostel "github.com/qiankunli/hostel/internal"
	apiview "github.com/qiankunli/hostel/internal/api/apiv1/view"
)

func respondError(c *gin.Context, status int, code apiview.ErrorCode, msg string) {
	respondFailure(c, status, code, msg, nil)
}

func respondFailure(c *gin.Context, status int, code apiview.ErrorCode, msg string, err error) {
	c.JSON(status, apiview.ErrorResponse{Code: code, Message: msg, Cause: hostel.CauseOf(err)})
}

func badRequest(c *gin.Context, msg string) {
	respondFailure(c, http.StatusBadRequest, apiview.ErrInvalidRequest, msg, hostel.WrapError(hostel.ErrInvalidArgument, "request", hostel.CauseRequest, nil))
}

func runtimeError(c *gin.Context, msg string) {
	respondError(c, http.StatusInternalServerError, apiview.ErrRuntimeError, msg)
}

func runtimeFailure(c *gin.Context, err error) {
	respondFailure(c, http.StatusInternalServerError, apiview.ErrRuntimeError, err.Error(), err)
}
