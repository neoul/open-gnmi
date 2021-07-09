package server

import (
	"fmt"
	"sync"

	"github.com/golang/glog"
	"github.com/neoul/gtrie"
	"github.com/neoul/yangtree"
	"github.com/openconfig/goyang/pkg/yang"
)

// EventType defines the type of the data update events in gNMI server.
type EventType int32

const (
	EventCreate  EventType = 0
	EventReplace EventType = 1
	EventDelete  EventType = 2
)

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

// Event Control Block
type EventReceiver interface {
	IsEventReceiver()
	EventReceive(uint, EventType, string)
	EventComplete(uint)
	EventPath() []string
}

type EventRecvGroup map[EventReceiver]EventReceiver

// gNMI Telemetry Control Block
type EventCtrl struct {
	Receivers  *gtrie.Trie // EventRecvGroup indexed by path
	Ready      map[EventReceiver]struct{}
	rootschema *yang.Entry
	mutex      *sync.Mutex
	eid        uint
}

func newEventCtrl(schema *yang.Entry) *EventCtrl {
	return &EventCtrl{
		Receivers:  gtrie.New(),
		Ready:      make(map[EventReceiver]struct{}),
		rootschema: schema,
		mutex:      &sync.Mutex{},
	}
}

func (ec *EventCtrl) Register(eReceiver EventReceiver) error {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()
	eventpath := eReceiver.EventPath()
	for i := range eventpath {
		if group, ok := ec.Receivers.Find(eventpath[i]); ok {
			group.(EventRecvGroup)[eReceiver] = eReceiver
		} else {
			ec.Receivers.Add(eventpath[i], EventRecvGroup{eReceiver: eReceiver})
		}
		if glog.V(11) {
			glog.Infof("event: %q registered by %q", eventpath[i], eReceiver)
		}
	}
	return nil
}

func (ec *EventCtrl) Unregister(eReceiver EventReceiver) {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()
	all := ec.Receivers.All()
	for eventpath, group := range all {
		egroup := group.(EventRecvGroup)
		if _, ok := egroup[eReceiver]; ok {
			if glog.V(11) {
				glog.Infof("event: %q unregistered by %q", eventpath, eReceiver)
			}
			delete(egroup, eReceiver)
			if len(egroup) == 0 {
				ec.Receivers.Remove(eventpath)
			}
		}
	}
	delete(ec.Ready, eReceiver)
}

// setReady() sets gnmi event.
func (ec *EventCtrl) setReady(event EventType, node []yangtree.DataNode) error {
	for i := range node {
		if glog.V(11) {
			glog.Infof("event: on-change in %q", node[i].Path())
		}
		if !yangtree.IsValid(node[i]) {
			return fmt.Errorf("invalid node inserted for gnmi update event")
		}
		for _, group := range ec.Receivers.FindAll(node[i].Path()) {
			egroup := group.(EventRecvGroup)
			for eReceiver := range egroup {
				ec.Ready[eReceiver] = struct{}{}
				eReceiver.EventReceive(ec.eid, event, node[i].Path())
			}
		}
		schema := node[i].Schema()
		if yangtree.HasUniqueListParent(schema) {
			schemapath := yangtree.GeneratePath(schema, false, false)
			for _, group := range ec.Receivers.FindAll(schemapath) {
				egroup := group.(EventRecvGroup)
				for eReceiver := range egroup {
					ec.Ready[eReceiver] = struct{}{}
					eReceiver.EventReceive(ec.eid, event, node[i].Path())
				}
			}
		}
	}
	return nil
}

// setReady() sets gnmi event.
func (ec *EventCtrl) setReadyByPath(event EventType, path []string) error {
	for i := range path {
		if glog.V(11) {
			glog.Infof("event: on-change in %q", path[i])
		}
		for _, group := range ec.Receivers.FindAll(path[i]) {
			egroup := group.(EventRecvGroup)
			for eReceiver := range egroup {
				ec.Ready[eReceiver] = struct{}{}
				eReceiver.EventReceive(ec.eid, event, path[i])
			}
		}
		schemapath, ok := yangtree.RemovePredicates(&(path[i]))
		if ok {
			for _, group := range ec.Receivers.FindAll(schemapath) {
				egroup := group.(EventRecvGroup)
				for eReceiver := range egroup {
					ec.Ready[eReceiver] = struct{}{}
					eReceiver.EventReceive(ec.eid, event, path[i])
				}
			}
		}
	}
	return nil
}

// SetEvent() sets gnmi event.
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
	for eReceiver := range ec.Ready {
		eReceiver.EventComplete(ec.eid)
		delete(ec.Ready, eReceiver)
	}
	return nil
}

// SetEventByPath() sets gnmi event.
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
	for eReceiver := range ec.Ready {
		eReceiver.EventComplete(ec.eid)
		delete(ec.Ready, eReceiver)
	}
	return nil
}
