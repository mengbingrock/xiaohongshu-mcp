package reddit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateProfileKey(t *testing.T) {
	key, err := ValidateProfileKey("a9c48d3a132290d60584a38d63ed59a5")
	require.NoError(t, err)
	assert.Equal(t, "a9c48d3a132290d60584a38d63ed59a5", key)
	_, err = ValidateProfileKey("../../cookies")
	assert.Error(t, err)
}

func TestNormalizeCommunity(t *testing.T) {
	name, err := NormalizeCommunity("/r/COROLLA")
	require.NoError(t, err)
	assert.Equal(t, "corolla", name)
	_, err = NormalizeCommunity("../COROLLA")
	assert.Error(t, err)
}

func TestNormalizeThingID(t *testing.T) {
	id, err := NormalizeThingID("1uyxj8j", "t3")
	require.NoError(t, err)
	assert.Equal(t, "t3_1uyxj8j", id)
	id, err = NormalizeThingID("t3_1UYXJ8J", "t3")
	require.NoError(t, err)
	assert.Equal(t, "t3_1uyxj8j", id)

	_, err = NormalizeThingID("t1_1uyxj8j", "t3")
	assert.Error(t, err)
}
