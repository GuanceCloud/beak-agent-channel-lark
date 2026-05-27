package basic

import (
	beaklark "github.com/GuanceCloud/beak-agent-channel-lark"
	"github.com/GuanceCloud/beak-agent-channel-lark/sdk"
)

func LarkConnector() sdk.Connector {
	return beaklark.NewConnector()
}
