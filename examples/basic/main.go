package basic

import (
	beaklark "beak-agent-lark"
	"beak-agent-lark/sdk"
)

func LarkConnector() sdk.Connector {
	return beaklark.NewConnector()
}
