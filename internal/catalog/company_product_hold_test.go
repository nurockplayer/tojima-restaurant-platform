package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCompanyHoldRejectsInvalidActorBeforeDatabaseAccess(t *testing.T) {
	// No database is attached: reaching Begin would panic instead of returning ErrInvalid.
	service := &Service{}
	for _, actor := range []string{"", " ", "admin\n", string([]byte{0xff}), strings.Repeat("a", 201), strings.Repeat("界", 201)} {
		for _, active := range []bool{true, false} {
			if _, err := service.SetCompanyProductEmergencyHold(context.Background(), "tenant", "tea", actor, active, "review", 0, "hold-key"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("actor %q active=%v: got %v, want ErrInvalid", actor, active, err)
			}
		}
	}
}
