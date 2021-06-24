package server

import (
	"fmt"

	gyangtree "github.com/neoul/yangtree/gnmi"
	"github.com/openconfig/gnmi/proto/gnmi"
)

const dynamicTeleSubInfoFormat = `
telemetry-system:
 subscriptions:
  dynamic-subscriptions:
   dynamic-subscription[id=%d]:
    id: %d
    state:
     id: %d
     destination-address: %s
     destination-port: %d
     sample-interval: %d
     heartbeat-interval: %d
     suppress-redundant: %v
     protocol: %s
     encoding: ENC_%s
     mode: %s`

const streamModeFormat = `
     stream-mode: %s`

const dynamicTeleSubInfoPathHeaderFormat = `
    sensor-paths:
`
const dynamicTeleSubInfoPathFormat = `
     sensor-path[path=%s]:
      state:
       path: %s`

func (s *Server) addDynamicSubscription(sub *Subscriber) {
	var data string
	switch sub.Mode {
	case gnmi.SubscriptionList_STREAM:
		data = fmt.Sprintf(dynamicTeleSubInfoFormat,
			sub.ID, sub.ID, sub.ID,
			sub.session.Address,
			sub.session.Port,
			sub.StreamConfig.SampleInterval,
			sub.StreamConfig.HeartbeatInterval,
			sub.StreamConfig.SuppressRedundant,
			"STREAM_GRPC",
			sub.Encoding,
			sub.Mode,
		) + fmt.Sprintf(streamModeFormat+dynamicTeleSubInfoPathHeaderFormat, sub.StreamMode)
	case gnmi.SubscriptionList_POLL:
		data = fmt.Sprintf(dynamicTeleSubInfoFormat,
			sub.ID, sub.ID, sub.ID,
			sub.session.Address,
			sub.session.Port,
			0,
			0,
			false,
			"STREAM_GRPC",
			sub.Encoding,
			sub.Mode,
		) + fmt.Sprintf(dynamicTeleSubInfoPathHeaderFormat)
	default:
		return
	}
	sub.mutex.Lock()
	for i := range sub.Paths {
		p := gyangtree.ToPath(true, sub.Paths[i])
		data += fmt.Sprintf(dynamicTeleSubInfoPathFormat, p, p)
	}
	sub.mutex.Unlock()
	// s.iStateUpdate.Write([]byte(data))
}

func (s *Server) deleteDynamicSubscriptionInfo(sub *Subscriber) {
	// 	data := fmt.Sprintf(`
	// telemetry-system:
	//  subscriptions:
	//   dynamic-subscriptions:
	//    dynamic-subscription[id=%d]:
	// `, sub.ID)
	// s.iStateUpdate.Delete([]byte(data))
}

// // GetInternalStateUpdate returns internal StateUpdate channel.
// func (s *Server) GetInternalStateUpdate() model.StateUpdate {
// 	return s.iStateUpdate
// }

// type mempprof struct {
// 	filename string
// 	index    int
// }

// func newMempprof() *mempprof {
// 	return &mempprof{}
// }

// func (m *mempprof) write(file string) {
// 	if m.filename == file {
// 		m.index++
// 	} else {
// 		m.filename = file
// 		m.index = 0
// 	}
// 	f, err := os.Create(fmt.Sprintf("%s.%d", m.filename, m.index))
// 	if err != nil {
// 		log.Fatal("could not create memory profile: ", err)
// 	}
// 	defer f.Close() // error handling omitted for example
// 	runtime.GC()    // get up-to-date statisticsd
// 	if err := pprof.WriteHeapProfile(f); err != nil {
// 		log.Fatal("could not write memory profile: ", err)
// 	}
// }

// var memp *mempprof

// func init() {
// 	memp = newMempprof()
// }
