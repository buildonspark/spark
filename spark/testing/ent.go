package sparktesting

import (
	"testing"
	"time"

	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/require"
)

func NewPostgresEntClient(t *testing.T, databaseURI string) *ent.Client {
	var entClient *ent.Client
	var err error
	for i := 0; i < 3; i++ {
		entClient, err = ent.Open("postgres", databaseURI)
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	require.NoError(t, err, "failed to connect to database")
	return entClient
}
