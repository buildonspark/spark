package grpctest

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark/so/dkg"
	testutil "github.com/lightsparkdev/spark/test_util"
)

// +-----------------------------------------------------------------+
// |                          IMPORTANT NOTICE                        |
// +-----------------------------------------------------------------+
// |                                                                  |
// |  THIS TEST IS BEING USED BY CI WORKFLOWS TO RUN THE DKG         |
// |  AS A PART OF SETTING UP THE INTEGRATION TEST ENVIRONMENT.       |
// |                                                                  |
// |  ██████╗  ██████╗     ███╗   ██╗ ██████╗ ████████╗              |
// |  ██╔══██╗██╔═══██╗    ████╗  ██║██╔═══██╗╚══██╔══╝              |
// |  ██║  ██║██║   ██║    ██╔██╗ ██║██║   ██║   ██║                 |
// |  ██║  ██║██║   ██║    ██║╚██╗██║██║   ██║   ██║                 |
// |  ██████╔╝╚██████╔╝    ██║ ╚████║╚██████╔╝   ██║                 |
// |  ╚═════╝  ╚═════╝     ╚═╝  ╚═══╝ ╚═════╝    ╚═╝                 |
// |                                                                  |
// |  ███╗   ███╗ ██████╗ ██████╗ ██╗███████╗██╗   ██╗               |
// |  ████╗ ████║██╔═══██╗██╔══██╗██║██╔════╝╚██╗ ██╔╝               |
// |  ██╔████╔██║██║   ██║██║  ██║██║█████╗   ╚████╔╝                |
// |  ██║╚██╔╝██║██║   ██║██║  ██║██║██╔══╝    ╚██╔╝                 |
// |  ██║ ╚═╝ ██║╚██████╔╝██████╔╝██║██║        ██║                  |
// |  ╚═╝     ╚═╝ ╚═════╝ ╚═════╝ ╚═╝╚═╝        ╚═╝                  |
// |                                                                  |
// +-----------------------------------------------------------------+
func TestDKG(t *testing.T) {
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	err = dkg.GenerateKeys(context.Background(), config, 1000)
	if err != nil {
		t.Fatal(err)
	}
}
