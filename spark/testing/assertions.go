package sparktesting

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
)

func AssertVerifiedPendingTransfer(t *testing.T, err error, leafPrivKeyMap map[string][]byte, nodeToSend *spark.TreeNode, newLeafPrivKey keys.Private) {
	require.NoError(t, err, "unable to verify pending transfer")
	require.Len(t, leafPrivKeyMap, 1)
	require.Equal(t, leafPrivKeyMap[nodeToSend.Id], newLeafPrivKey.Serialize(), "wrong leaf signing private key")
}
