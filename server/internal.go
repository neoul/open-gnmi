package server

import (
	"fmt"
	"strconv"

	"github.com/neoul/yangtree"
	"github.com/openconfig/gnmi/proto/gnmi"
)

func (s *Server) addDynamicSubscription(subscriber *Subscriber) error {
	var err error
	var node yangtree.DataNode

	schema := yangtree.FindSchema(s.RootSchema,
		"/telemetry-system/subscriptions/dynamic-subscriptions/dynamic-subscription")
	if schema == nil {
		return fmt.Errorf("telemetry model not found")
	}

	subscriber.mutex.Lock()
	defer subscriber.mutex.Unlock()
	jsonstr := `{
		"id": %d,
		"state": {
			"id": %d,
			"destination-address": %q,
			"destination-port": %d,
			"sample-interval": %d,
			"heartbeat-interval": %d,
			"suppress-redundant": %v,
			"protocol": %q,
			"mode": %q
		}
	}`
	switch subscriber.Mode {
	case gnmi.SubscriptionList_STREAM:
		node, err = yangtree.New(schema, fmt.Sprintf(jsonstr,
			subscriber.ID, subscriber.ID,
			subscriber.session.Address,
			subscriber.session.Port,
			subscriber.StreamConfig.SampleInterval,
			subscriber.StreamConfig.HeartbeatInterval,
			subscriber.StreamConfig.SuppressRedundant,
			"STREAM_GRPC",
			subscriber.Mode))
		if err != nil {
			return err
		}
		err = yangtree.Set(node, "state/encoding", "ENC_"+subscriber.Encoding.String())
		if err != nil {
			return err
		}
		err = yangtree.Set(node, "state/stream-mode", subscriber.StreamMode.String())
		if err != nil {
			return err
		}
	case gnmi.SubscriptionList_POLL:
		node, err = yangtree.New(schema, fmt.Sprintf(jsonstr,
			subscriber.ID, subscriber.ID,
			subscriber.session.Address,
			subscriber.session.Port,
			0,
			0,
			false,
			"STREAM_GRPC",
			subscriber.Mode))
		if err != nil {
			return err
		}
		err = yangtree.Set(node, "state/encoding", "ENC_"+subscriber.Encoding.String())
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid subscription mode")
	}
	for i := range subscriber.Path {
		p := subscriber.Prefix + subscriber.Path[i]
		err = yangtree.Set(node, "sensor-paths/sensor-path[path="+p+"]/state/path", p)
		if err != nil {
			return err
		}
	}

	path := "/telemetry-system/subscriptions/dynamic-subscriptions/dynamic-subscription[id=" +
		strconv.FormatUint(uint64(subscriber.ID), 10) + "]"
	return s.Merge(path, node)
}

func (s *Server) deleteDynamicSubscriptionInfo(subscriber *Subscriber) error {
	path := "/telemetry-system/subscriptions/dynamic-subscriptions/dynamic-subscription[id=" +
		strconv.FormatUint(uint64(subscriber.ID), 10) + "]"
	return s.Delete(path)
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
