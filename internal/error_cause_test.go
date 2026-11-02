package hostel

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestFailureAttributionSurvivesContext(t *testing.T) {
	leaf := WrapError(ErrUnavailable, "forwarding", CauseCarrier, os.ErrPermission)
	err := WrapError(ErrPreparationFailed, "bed initialization", CauseUnknown, fmt.Errorf("prepare: %w", leaf))
	if CauseOf(err) != CauseCarrier || !errors.Is(err, ErrUnavailable) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("lost cause or identity: %v, %s", err, CauseOf(err))
	}
	// Classification follows evidence, not the operation name or error wording.
	if CauseOf(fmt.Errorf("bed carrier permission denied")) != CauseUnknown {
		t.Fatal("guessed cause")
	}
	if CauseOf(&Error{Kind: ErrUnavailable, Op: "bed", Err: leaf}) != CauseCarrier {
		t.Fatal("zero-value wrapper hid cause")
	}
}

func TestJoinedFailureAttribution(t *testing.T) {
	carrier := WrapError(ErrUnavailable, "forwarding", CauseCarrier, nil)
	bed := WrapError(ErrPreparationFailed, "namespace verification", CauseBed, nil)
	for _, tc := range []struct {
		err  error
		want ErrorCause
	}{
		{nil, CauseUnknown}, {errors.Join(carrier, nil), CauseCarrier},
		{errors.Join(carrier, carrier), CauseCarrier}, {errors.Join(carrier, bed), CauseUnknown},
		{errors.Join(carrier, errors.New("cleanup failed")), CauseUnknown},
	} {
		if got := CauseOf(tc.err); got != tc.want {
			t.Fatalf("%v: got %s want %s", tc.err, got, tc.want)
		}
	}
}
