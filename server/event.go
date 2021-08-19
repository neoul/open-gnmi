package server

import (
	"fmt"
	"strings"
	"sync"

	"github.com/golang/glog"
	"github.com/neoul/gtrie"
	"github.com/neoul/yangtree"
	"github.com/openconfig/goyang/pkg/yang"
)

// EventType defines the type of the data update events of the gNMI server.
// EventCreate - a data node is created.
// EventReplace - a data node is replaced.
// EventDelete - a data node is deleted.
type EventType int32

const (
	EventCreate  EventType = 0 // a data node is created.
	EventReplace EventType = 1 // a data node is replaced.
	EventDelete  EventType = 2 // a data node is deleted.
)

// String returns a string of the EventType
func (x EventType) String() string {
	switch x {
	case EventCreate:
		return "create"
	case EventReplace:
		return "replace"
	case EventDelete:
		return "delete"
	default:
		return "?"
	}
}

// EventReceiver is an interface that is invoked
// by gNMI server when a data node is changed.
// gNMI telemetry update (SubscribeResponse) messages are generated
// by the built-in EventReceiver of the subscribe RPC procssing.
// Customer EventReceiver can be registered if the folling functions are implemented.
//  - EventStart indicates the start of a set of events the the EventReceiver.
//  - EventComplete indicates the end of the set of events to the EventReceiver.
//  - EventReceive must process each event to be raised.
//  - EventPath must return the paths on which the events become raised.
type EventReceiver interface {
	EventStart(tid uint)                               // EventStart is invoked at the start of a set of events.
	EventReceive(tid uint, typ EventType, path string) // EventComplete is invoked at the end of a set of events.
	EventComplete(tid uint)                            // EventReceive is invoked to process the each event to be raised.
	EventPath() []string                               // EventPath is used to register the path of the event.
}

type EventRecvGroup map[EventReceiver]EventReceiver

// EventCtrl (Event Control Block) controls a set of events
// that is the update of the data nodes in the gNMI server.
type EventCtrl struct {
	receivers  *gtrie.Trie // EventRecvGroup indexed by the event path
	ready      map[EventReceiver]struct{}
	rootschema *yang.Entry
	mutex      *sync.Mutex
	eid        uint
}

func newEventCtrl(schema *yang.Entry) *EventCtrl {
	return &EventCtrl{
		receivers:  gtrie.New(),
		ready:      make(map[EventReceiver]struct{}),
		rootschema: schema,
		mutex:      &sync.Mutex{},
	}
}

func (ec *EventCtrl) String() string {
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("%v", ec.receivers.All()))
	return buf.String()
}

// Register() is used to register an EventReceiver. During the registration,
// the EventPath() of the EventReciver is invoked to register the event path.
func (ec *EventCtrl) Register(eReceiver EventReceiver) error {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()
	eventpath := eReceiver.EventPath()
	for i := range eventpath {
		if group, ok := ec.receivers.Find(eventpath[i]); ok {
			group.(EventRecvGroup)[eReceiver] = eReceiver
		} else {
			ec.receivers.Add(eventpath[i], EventRecvGroup{eReceiver: eReceiver})
		}
		if glog.V(11) {
			glog.Infof("event: %q registered by %q", eventpath[i], eReceiver)
		}
	}
	return nil
}

// Unregister() is used to unregister the EventReceiver. The EventReciver
// is removed from the EventCtrl (Event Control Block).
func (ec *EventCtrl) Unregister(eReceiver EventReceiver) {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()
	allEventRecvGroups := ec.receivers.All()
	for eventpath, group := range allEventRecvGroups {
		egroup := group.(EventRecvGroup)
		if _, ok := egroup[eReceiver]; ok {
			if glog.V(11) {
				glog.Infof("event: %q unregistered by %q", eventpath, eReceiver)
			}
			delete(egroup, eReceiver)
			if len(egroup) == 0 {
				ec.receivers.Remove(eventpath)
			}
		}
	}
	delete(ec.ready, eReceiver)
}

// SetEvent() and SetEventByPath() generate the events regarding to the updated data nodes.
func (ec *EventCtrl) SetEvent(c, r, d []yangtree.DataNode) error {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()
	ec.eid++
	if err := ec.setReady(EventCreate, c); err != nil {
		return err
	}
	if err := ec.setReady(EventReplace, r); err != nil {
		return err
	}
	if err := ec.setReady(EventDelete, d); err != nil {
		return err
	}
	// current event consumed at the end.
	for eReceiver := range ec.ready {
		eReceiver.EventComplete(ec.eid)
		delete(ec.ready, eReceiver)
	}
	return nil
}

// SetEvent() and SetEventByPath() generate the events regarding to the updated data nodes.
func (ec *EventCtrl) SetEventByPath(c, r, d []string) error {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()
	ec.eid++
	if err := ec.setReadyByPath(EventCreate, c); err != nil {
		return err
	}
	if err := ec.setReadyByPath(EventReplace, r); err != nil {
		return err
	}
	if err := ec.setReadyByPath(EventDelete, d); err != nil {
		return err
	}
	// current event consumed at the end.
	for eReceiver := range ec.ready {
		eReceiver.EventComplete(ec.eid)
		delete(ec.ready, eReceiver)
	}
	return nil
}

func (ec *EventCtrl) setReady(event EventType, node []yangtree.DataNode) error {
	for i := range node {
		if glog.V(11) {
			glog.Infof("event: on-change in %q", node[i].Path())
		}
		if !yangtree.IsValid(node[i]) {
			return fmt.Errorf("invalid node inserted for gnmi update event")
		}
		for _, group := range ec.receivers.FindAll(node[i].Path()) {
			egroup := group.(EventRecvGroup)
			for eReceiver := range egroup {
				if _, ok := ec.ready[eReceiver]; !ok {
					ec.ready[eReceiver] = struct{}{}
					eReceiver.EventStart(ec.eid)
				}
				eReceiver.EventReceive(ec.eid, event, node[i].Path())
			}
		}
		schema := node[i].Schema()
		if yangtree.HasUniqueListParent(schema) {
			schemapath := yangtree.GeneratePath(schema, false, false)
			fmt.Println(schemapath)
			for _, group := range ec.receivers.FindAll(schemapath) {

				egroup := group.(EventRecvGroup)
				for eReceiver := range egroup {
					if _, ok := ec.ready[eReceiver]; !ok {
						ec.ready[eReceiver] = struct{}{}
						eReceiver.EventStart(ec.eid)
					}
					eReceiver.EventReceive(ec.eid, event, node[i].Path())
				}
			}
		}
	}
	return nil
}

func (ec *EventCtrl) setReadyByPath(event EventType, path []string) error {
	for i := range path {
		if glog.V(11) {
			glog.Infof("event: on-change in %q", path[i])
		}
		for _, group := range ec.receivers.FindAll(path[i]) {
			egroup := group.(EventRecvGroup)
			for eReceiver := range egroup {
				if _, ok := ec.ready[eReceiver]; !ok {
					ec.ready[eReceiver] = struct{}{}
					eReceiver.EventStart(ec.eid)
				}
				eReceiver.EventReceive(ec.eid, event, path[i])
			}
		}
		schemapath, ok := yangtree.RemovePredicates(&(path[i]))
		if ok {
			for _, group := range ec.receivers.FindAll(schemapath) {
				egroup := group.(EventRecvGroup)
				for eReceiver := range egroup {
					if _, ok := ec.ready[eReceiver]; !ok {
						ec.ready[eReceiver] = struct{}{}
						eReceiver.EventStart(ec.eid)
					}
					eReceiver.EventReceive(ec.eid, event, path[i])
				}
			}
		}
	}
	return nil
}
