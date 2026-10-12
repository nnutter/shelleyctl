package instance

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeleteRefusesConflictingContainerBeforeOtherMutations(t *testing.T) {
	conflict := errors.New("container belongs to another state directory")
	var actions []string
	a := testApplication(t, func(action string) error { actions = append(actions, action); return conflict })
	seed(t, a, 19000, true)
	require.ErrorIs(t, a.Delete(t.Context(), "demo"), conflict)
	require.Equal(t, []string{"check container"}, actions)
	require.False(t, diskRecord(t, a).Desired)
}
