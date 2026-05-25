package lspapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyAndValidateRGBAssignmentDefaultsToValue(t *testing.T) {
	params := &RGBInvoiceInput{}
	require.NoError(t, applyAndValidateRGBAssignment(params, "Any"))
	require.NotNil(t, params.Assignment)
	require.Equal(t, "Any", *params.Assignment)
}

func TestApplyAndValidateRGBAssignmentNormalizesCase(t *testing.T) {
	in := "value"
	params := &RGBInvoiceInput{Assignment: &in}
	require.NoError(t, applyAndValidateRGBAssignment(params, "Any"))
	require.NotNil(t, params.Assignment)
	require.Equal(t, "Any", *params.Assignment)
}

func TestApplyAndValidateRGBAssignmentRejectsUnsupported(t *testing.T) {
	in := "Other"
	params := &RGBInvoiceInput{Assignment: &in}
	require.Error(t, applyAndValidateRGBAssignment(params, "Any"))
}

func TestRgbAssignmentJSONAnyValueAlias(t *testing.T) {
	v := "Value"
	got, err := rgbAssignmentJSON(&v)
	require.NoError(t, err)
	require.Equal(t, "Any", got["type"])
}
