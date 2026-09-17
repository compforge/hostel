package hostel

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCategoryAndCauseSurviveWrapping(t *testing.T) {
	cause := &os.PathError{Op: "open", Path: "/private/carrier/path", Err: os.ErrNotExist}
	wrapped := fmt.Errorf("execute: %w", WrapError(ErrPreparationFailed, "session directory", cause))
	if !errors.Is(wrapped, ErrPreparationFailed) || !errors.Is(wrapped, os.ErrNotExist) || errors.Is(wrapped, ErrConflict) {
		t.Fatal(wrapped)
	}
	var detail *Error
	if !errors.As(wrapped, &detail) || detail.Op != "session directory" {
		t.Fatal(wrapped)
	}
	var pathError *os.PathError
	if !errors.As(wrapped, &pathError) {
		t.Fatal("underlying error lost")
	}
	if strings.Contains(detail.Message(), cause.Path) || !strings.Contains(wrapped.Error(), cause.Path) {
		t.Fatal("public/internal messages crossed")
	}
}
