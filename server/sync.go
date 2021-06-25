package server

import (
	"time"

	"github.com/golang/glog"
	"github.com/neoul/gtrie"
	"github.com/neoul/open-gnmi/utilities/status"
	"github.com/neoul/yangtree"
	gyangtree "github.com/neoul/yangtree/gnmi"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
	"github.com/openconfig/goyang/pkg/yang"
	"google.golang.org/grpc/codes"
)

// SyncCallback is a callback interface invoked by the gNMI server to request
// the data updates of the specified path to the system.
// Upon gNMI Get or Subscribe request, the SyncCallback will be invoked by the server.
// The user must define and configure the SyncCallback interface if the synchronization of
// the data is required for some special paths before the gNMI Get and Subscribe RPC responses.
type SyncCallback interface {
	SyncCallback(path ...string) error
}

// SyncCallbackOption is used to configure the data synchronization options
// of the gNMI server at startup. SyncMinInterval is the minimum interval
// between two sync requests for each specified path. SyncCallback is the callback
// interface invoked by the gNMI server upon the sync request to request the data updates
// of the specified path to the system.
type SyncCallbackOption struct {
	SyncCallback
	MinInterval time.Duration
}

// IsOption - the option of the gNMI server.
func (o SyncCallbackOption) IsOption() {}

func hasSyncCallback(opts []Option) SyncCallback {
	for _, o := range opts {
		switch v := o.(type) {
		case SyncCallbackOption:
			return v.SyncCallback
		}
	}
	return nil
}

func hasSyncMinInterval(opts []Option) time.Duration {
	for _, o := range opts {
		switch v := o.(type) {
		case SyncCallbackOption:
			if v.MinInterval == 0 {
				return time.Second
			}
			return v.MinInterval
		}
	}
	return time.Second
}

type syncTime struct {
	time.Time
}

func (s *Server) syncInit(opts ...Option) {
	s.syncMinInterval = hasSyncMinInterval(opts)
	s.syncCallback = hasSyncCallback(opts)
	s.syncPath = gtrie.New()
}

type syncEvent struct {
	regPath  string
	schema   *yang.Entry
	syncPath *gtrie.Trie
	*Server
}

func (e *syncEvent) String() string { return "sync." + e.regPath }

// IsEventReceiver is the EventReceiver interface for sync
func (e *syncEvent) IsEventReceiver() {}

// EventComplete is the EventReceiver interface for sync
func (e *syncEvent) EventComplete(interface{}) bool {
	return true
}

// EventReceive is the EventReceiver interface for sync
func (e *syncEvent) EventReceive(edata interface{}, event ChangeEvent, path string) interface{} {
	if yangtree.FindSchema(e.RootSchema, path) == e.schema {
		switch event {
		case EventCreate:
			if glog.V(10) {
				glog.Infof("sync: add path %q", path)
			}
			e.syncPath.Add(path, &syncTime{})
		case EventDelete:
			if glog.V(10) {
				glog.Infof("sync: delete path %q", path)
			}
			e.syncPath.Remove(path)
		}
	}
	return nil
}

// EventPath is the EventReceiver interface for sync
func (e *syncEvent) EventPath() []string {
	return []string{e.regPath}
}

// RegisterSync() is used to set the sync-required paths.
// When the data of the sync-required paths are retrieved by the Get or Subscribe RPC,
// the SyncCallback interface is invoked by the gNMI server for the data synchronization.
func (s *Server) RegisterSync(path ...string) error {
	s.Lock()
	defer s.Unlock()
	for i := range path {
		schema := yangtree.FindSchema(s.RootSchema, path[i])
		if schema == nil {
			err := status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"schema not found for %q", path[i])
			if glog.V(10) {
				glog.Errorf("sync: registering error: %v", err)
			}
			return err
		}
		event := &syncEvent{
			regPath:  path[i],
			schema:   schema,
			syncPath: s.syncPath,
			Server:   s,
		}

		if err := s.Event.Register(event); err != nil {
			if glog.V(10) {
				glog.Errorf("sync: registering error: %v", err)
			}
			return err
		}
		s.syncEvents = append(s.syncEvents, event)
		node, _ := yangtree.Find(s.Root, path[i])
		for j := range node {
			if glog.V(10) {
				glog.Infof("sync: add path %q", node[j].Path())
			}
			s.syncPath.Add(node[j].Path(), &syncTime{})
		}
	}
	return nil
}

// UnregisterSync() is used to remove the sync-required paths.
// When the data of the sync-required paths are retrieved by the Get or Subscribe RPC,
// the SyncCallback interface is invoked by the gNMI server for the data synchronization.
func (s *Server) UnregisterSync(path ...string) error {
	s.Lock()
	defer s.Unlock()
	for i := range path {
		schema := yangtree.FindSchema(s.RootSchema, path[i])
		if schema == nil {
			err := status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"schema not found for %q", path[i])
			if glog.V(10) {
				glog.Errorf("sync: unregistering error: %v", err)
			}
			return err
		}

		var event *syncEvent
		for i := range s.syncEvents {
			if s.syncEvents[i].schema == schema {
				event = s.syncEvents[i]
				s.syncEvents = append(s.syncEvents[:i], s.syncEvents[i+1:]...)
				break
			}
		}
		if event == nil {
			err := status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"event not found for %s", path[i])
			if glog.V(10) {
				glog.Errorf("sync: unregistering error: %v", err)
			}
			return err
		}
		s.Event.Unregister(event)
	}
	s.syncPath.Clear()
	if glog.V(10) {
		glog.Infof("sync.delete all paths are deleted")
	}
	for i := range s.syncEvents {
		node, _ := yangtree.Find(s.Root, s.syncEvents[i].regPath)
		for j := range node {
			if glog.V(10) {
				glog.Infof("sync: add path %q", node[j].Path())
			}
			s.syncPath.Add(node[j].Path(), &syncTime{})
		}
	}
	return nil
}

func (s *Server) syncExec(syncpaths map[string]interface{}) {
	if len(syncpaths) == 0 {
		return
	}
	cur := time.Now()
	tosend := make([]string, 0, len(syncpaths))
	for p, stime := range syncpaths {
		if synctime, ok := stime.(*syncTime); ok {
			diff := cur.Sub(synctime.Time)
			if diff > s.syncMinInterval || diff < (-1*s.syncMinInterval) {
				synctime.Time = time.Now()
				tosend = append(tosend, p)
			} else {
				if glog.V(10) {
					glog.Infof("sync: sync path %q is filtered by sync-min-interval", p)
				}
			}
		}
	}
	s.syncCallback.SyncCallback(tosend...)
}

// syncRequest requests the data sync to the system before read.
// Do not use server.Lock() before it because it updates the server.Root.
func (s *Server) syncRequest(prefix *gnmipb.Path, paths []*gnmipb.Path) {
	if s.syncCallback == nil {
		return
	}
	for _, path := range paths {
		fullpath := gyangtree.MergeGNMIPath(prefix, path)
		if glog.V(10) {
			glog.Infof("sync: request %q", gyangtree.ToPath(true, fullpath))
		}
		reqpath := gyangtree.FindPaths(s.RootSchema, fullpath)
		for i := range reqpath {
			syncpath := s.syncPath.SearchAll(reqpath[i], gtrie.SearchAllRelativeKey)
			s.syncExec(syncpath)
		}
	}
}
