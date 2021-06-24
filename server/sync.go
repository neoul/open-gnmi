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

// SyncCallback is the callback interface invoked by the gNMI server.
// in order to request the data updates of the specified path to the system.
// Upon gNMI Get or Subscribe request, the SyncCallback will be invoked by the server.
// the SyncCallback must update the data tree based on the sync-requested paths.
type SyncCallback interface {
	SyncCallback(path ...string) error
}

// SyncCallbackOption is used to configure the sync  the gNMI server
// SyncMinInterval is the minimum time of state sync interval (unit: sec)
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
			return v.MinInterval * time.Second
		}
	}
	return 1 * time.Second
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

func (e *syncEvent) String() string   { return "sync." + e.regPath }
func (e *syncEvent) IsEventReceiver() {}
func (e *syncEvent) EventComplete(interface{}) bool {
	return true
}

func (e *syncEvent) EventReceive(edata interface{}, event ChangeEvent, path string) interface{} {
	if yangtree.FindSchema(e.RootSchema, path) == e.schema {
		switch event {
		case EventCreate:
			e.syncPath.Add(path, &syncTime{})
		case EventDelete:
			e.syncPath.Remove(path)
		}
	}
	return nil
}

func (e *syncEvent) EventPath() []string {
	return []string{e.regPath}
}

// AddSyncPath() is used to set the sync-requested path.
func (s *Server) AddSyncPath(path ...string) error {
	for i := range path {
		schema := yangtree.FindSchema(s.RootSchema, path[i])
		if schema == nil {
			err := status.TaggedErrorf(codes.Internal, status.TagOperationFail, "%q not found", path[i])
			if glog.V(10) {
				glog.Errorf("sync: %v", err)
			}
			return err
		}
		event := &syncEvent{
			regPath:  path[i],
			schema:   schema,
			syncPath: s.syncPath,
			Server:   s,
		}
		node, _ := yangtree.Find(s.Root, path[i])
		for j := range node {
			s.syncPath.Add(node[j].Path(), &syncTime{})
		}
		if err := s.Event.Register(event); err != nil {
			if glog.V(10) {
				glog.Errorf("sync: %v", err)
			}
			return err
		}
		if glog.V(10) {
			glog.Infof("sync: %q added", path[i])
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
					glog.Infof("sync: %q: filtered by sync-min-interval", p)
				}
			}
		}
	}
	if glog.V(10) {
		for _, p := range tosend {
			glog.Infof("sync: %q", p)
		}
	}

	s.syncCallback.SyncCallback(tosend...)
}

// syncRequest requests the data sync to the system before read. (do not use Lock() before it.)
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
