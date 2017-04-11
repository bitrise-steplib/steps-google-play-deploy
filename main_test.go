package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetKeyPath(t *testing.T) {

	t.Log("GetKeyPath - file://../../../../../../Downloads/key.json")
	{
		keyPth, isRemote, err := getKeyPath("file://../../../../../../Downloads/key.json")
		require.NoError(t, err)

		require.Equal(t, "../../../../../../Downloads/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("GetKeyPath - file://./")
	{
		keyPth, isRemote, err := getKeyPath("file://./testfolder/key.json")
		require.NoError(t, err)

		require.Equal(t, "./testfolder/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("GetKeyPath - file:///")
	{
		keyPth, isRemote, err := getKeyPath("file:///testfolder/key.json")
		require.NoError(t, err)

		require.Equal(t, "/testfolder/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("GetKeyPath - http://")
	{
		keyPth, isRemote, err := getKeyPath("http://testdomain.com/testsub/key.json")
		require.NoError(t, err)

		require.Equal(t, "http://testdomain.com/testsub/key.json", keyPth)
		require.Equal(t, true, isRemote)
	}

	t.Log("GetKeyPath - https://")
	{
		keyPth, isRemote, err := getKeyPath("https://testdomain.com/testsub/key.json")
		require.NoError(t, err)

		require.Equal(t, "https://testdomain.com/testsub/key.json", keyPth)
		require.Equal(t, true, isRemote)
	}

	t.Log("GetKeyPath - ./")
	{
		keyPth, isRemote, err := getKeyPath("./user/test/key.json")
		require.NoError(t, err)

		require.Equal(t, "./user/test/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}

	t.Log("GetKeyPath - /")
	{
		keyPth, isRemote, err := getKeyPath("/user/test/key.json")
		require.NoError(t, err)

		require.Equal(t, "/user/test/key.json", keyPth)
		require.Equal(t, false, isRemote)
	}
}
