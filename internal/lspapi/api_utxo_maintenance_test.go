package lspapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUtxoMaintenanceDecisionDisabled(t *testing.T) {
	create, num, err := utxoMaintenanceDecision(0, 0)
	require.NoError(t, err)
	require.False(t, create)
	require.Zero(t, num)
}

func TestUtxoMaintenanceDecisionValid(t *testing.T) {
	create, num, err := utxoMaintenanceDecision(3, 10)
	require.NoError(t, err)
	require.True(t, create)
	require.EqualValues(t, 7, num)
}

func TestUtxoMaintenanceDecisionInvalidRange(t *testing.T) {
	_, _, err := utxoMaintenanceDecision(5, 5)
	require.Error(t, err)
}
