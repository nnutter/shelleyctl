package instance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateRejectsNamesThatCannotBecomeServiceHostnames(t *testing.T) {
	for _, name := range []string{"alice-", "Alice", "../alice", "-alice", ""} {
		t.Run(name, func(t *testing.T) {
			var actions []string
			a := testApplication(t, func(action string) error { actions = append(actions, action); return nil })
			require.Error(t, a.Create(t.Context(), name))
			require.Empty(t, actions)
			require.NoFileExists(t, a.opts.StateDir+"/state.json")
		})
	}
}
