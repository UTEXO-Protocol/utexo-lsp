package lspapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyBackendMinConfirmationsAlwaysOverridesInput(t *testing.T) {
	params := &RGBInvoiceInput{MinConfirmations: 9}
	applyBackendMinConfirmations(params, 1)
	require.EqualValues(t, 1, params.MinConfirmations)
}

func TestApplyBackendMinConfirmationsNilSafe(t *testing.T) {
	applyBackendMinConfirmations(nil, 1)
}
