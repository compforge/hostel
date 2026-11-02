package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	hostel "github.com/qiankunli/hostel/internal"
)

func TestErrorResponseAttribution(t *testing.T) {
	for _, cause := range []hostel.ErrorCause{hostel.CauseCarrier, hostel.CauseBed, hostel.CauseRequest, hostel.CauseResource, hostel.CauseUnknown} {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		err := fmt.Errorf("context: %w", hostel.WrapError(hostel.ErrUnavailable, "test", cause, nil))
		runtimeFailure(ctx, err)
		var result struct {
			Cause hostel.ErrorCause `json:"cause"`
		}
		if e := json.Unmarshal(rec.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if rec.Code != 500 || result.Cause != cause {
			t.Fatalf("%s", rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	badRequest(ctx, "invalid body")
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	var result struct {
		Cause hostel.ErrorCause `json:"cause"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Cause != hostel.CauseRequest {
		t.Fatal(rec.Body)
	}
}
