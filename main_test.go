package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseKeyPath(t *testing.T) {

	t.Log("prepareKeyPath - file://../../../../../../Downloads/key.json")
	{
		keyPth, isRemote, err := prepareKeyPath("file://../../../../../../Downloads/key.json")
		require.NoError(t, err)

		require.Equal(t, "../../../../../../Downloads/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("prepareKeyPath - file://./")
	{
		keyPth, isRemote, err := prepareKeyPath("file://./testfolder/key.json")
		require.NoError(t, err)

		require.Equal(t, "./testfolder/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("prepareKeyPath - file:///")
	{
		keyPth, isRemote, err := prepareKeyPath("file:///testfolder/key.json")
		require.NoError(t, err)

		require.Equal(t, "/testfolder/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("prepareKeyPath - http://")
	{
		keyPth, isRemote, err := prepareKeyPath("http://testdomain.com/testsub/key.json")
		require.NoError(t, err)

		require.Equal(t, "http://testdomain.com/testsub/key.json", keyPth)
		require.Equal(t, true, isRemote)
	}

	t.Log("prepareKeyPath - https://")
	{
		keyPth, isRemote, err := prepareKeyPath("https://testdomain.com/testsub/key.json")
		require.NoError(t, err)

		require.Equal(t, "https://testdomain.com/testsub/key.json", keyPth)
		require.Equal(t, true, isRemote)
	}

	t.Log("prepareKeyPath - ./")
	{
		keyPth, isRemote, err := prepareKeyPath("./user/test/key.json")
		require.NoError(t, err)

		require.Equal(t, "./user/test/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("prepareKeyPath - /")
	{
		keyPth, isRemote, err := prepareKeyPath("/user/test/key.json")
		require.NoError(t, err)

		require.Equal(t, "/user/test/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}
}
