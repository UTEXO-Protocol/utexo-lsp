package lspapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureAssetSupported(t *testing.T) {
	a := &API{
		cfg: Config{
			SupportedAssetIDs: []string{"assetA", "assetB"},
		},
	}

	require.NoError(t, a.ensureAssetSupported("assetA"))
	require.Error(t, a.ensureAssetSupported("assetX"))
}

func TestEnsureAssetSupportedRequiresConfig(t *testing.T) {
	a := &API{cfg: Config{}}
	require.Error(t, a.ensureAssetSupported("assetA"))
}

func TestIsSupportedAssetAllowsBTC(t *testing.T) {
	a := &API{
		cfg: Config{
			SupportedAssetIDs: []string{"assetA"},
		},
	}

	require.True(t, a.isSupportedAsset(nil))

	empty := "   "
	require.True(t, a.isSupportedAsset(&empty))
}

func TestIsSupportedAssetForRGBRequiresAllowlist(t *testing.T) {
	a := &API{
		cfg: Config{
			SupportedAssetIDs: []string{"assetA"},
		},
	}

	assetA := "assetA"
	require.True(t, a.isSupportedAsset(&assetA))

	assetB := "assetB"
	require.False(t, a.isSupportedAsset(&assetB))
}
