package server

import (
	"fmt"

	"github.com/golang/glog"
	"github.com/neoul/open-gnmi/utilities/status"
	"github.com/neoul/yangtree"
	gyangtree "github.com/neoul/yangtree/gnmi"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
	"github.com/openconfig/goyang/pkg/yang"
	"google.golang.org/grpc/codes"
)

type setEntry struct {
	op       gnmipb.UpdateResult_Operation
	path     string
	cur      yangtree.DataNode
	new      yangtree.DataNode
	executed bool
}

// setinit initializes the Set transaction.
func (s *Server) setinit() error {
	s.setSeq++
	s.setBackup = nil
	return nil
}

// setdone clears the rollback data and resets all configuration in order to receive updates from the system.
func (s *Server) setdone() {
	if !s.setUpdatedByServer {
		// The set data is reverted to the previous data if the setUpdatedByServer is disabled.
		// The set data must be updated by the system, not the gnmi server.
		for _, e := range s.setBackup {
			if glog.V(10) {
				glog.Infof("set.done %s,%q", e.op, e.path)
			}
			if e.path == "" || e.path == "/" { // root
				s.Root = e.cur
			} else {
				err := yangtree.Replace(s.Root, e.path, e.cur)
				if err != nil {
					fmt.Println(err)
					if glog.V(10) {
						glog.Error(status.TaggedErrorf(codes.Internal, status.TagOperationFail,
							"set.done %s: %v", e.path, err))
					}
				}
			}
		}
	}
	s.setBackup = nil
}

// setrollback executes setIndirect to revert the configuration.
func (s *Server) setrollback() {
	for _, e := range s.setBackup {
		if glog.V(10) {
			glog.Infof("set.rollback %q", e.path)
		}
		// error is ignored on rollback
		if err := s.execCallback(e, true); err != nil {
			if glog.V(10) {
				glog.Error(status.TaggedErrorf(codes.Internal, status.TagOperationFail,
					"set.rollback %q: %v", e.path, err))
			}
		}
	}
	s.setBackup = nil
}

// setdelete deletes the data nodes of the path.
// 3.4.6 Deleting Configuration
//  - All descendants of the target path are recursively deleted.
//  - Wildcards (*, ...) are supported.
//  - Non-existent node deletion is accepted.
func (s *Server) setdelete(prefix, path *gnmipb.Path) error {
	gpath := gyangtree.MergeGNMIPath(prefix, path)
	if glog.V(10) {
		glog.Infof("set.delete %q", gyangtree.ToPath(true, gpath))
	}
	if err := gyangtree.ValidateGNMIPath(s.RootSchema, gpath); err != nil {
		return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
			"set.delete: invalid path: %v", err)
	}
	_path := gyangtree.ToPath(true, gpath)
	nodes, _ := yangtree.Find(s.Root, _path)
	for _, node := range nodes {
		path := node.Path()
		if node.Schema().ReadOnly() {
			return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
				"set.delete %q: unable to set read-only data", path)
		}
		if node == s.Root { // create new root if it is the root
			var err error
			s.Root, err = yangtree.New(node.Schema())
			if err != nil {
				return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
					"set.delete %q: %v", path, err)
			}
		}
		if err := node.Remove(); err != nil {
			return status.TaggedErrorf(codes.InvalidArgument, status.TagBadData,
				"set.delete %q: %v", path, err)
		}
		e := &setEntry{op: gnmipb.UpdateResult_DELETE, path: path, cur: node, new: nil}
		if err := s.execCallback(e, false); err != nil {
			return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.delete %q: %v", path, err)
		}
	}
	return nil
}

// setreplace deletes the path from root if the path exists.
// 3.4.4 Modes of Update: Replace versus Update
//  1. converts the typed value to new data node with the default value.
//  2. replace the cur data node to the new data node.
func (s *Server) setreplace(prefix, path *gnmipb.Path, typedvalue *gnmipb.TypedValue) error {
	gpath := gyangtree.MergeGNMIPath(prefix, path)
	if glog.V(10) {
		glog.Infof("set.replace %q", gyangtree.ToPath(true, gpath))
	}
	if err := gyangtree.ValidateGNMIPath(s.RootSchema, gpath); err != nil {
		return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
			"set.replace: invalid path: %v", err)
	}

	var err error
	var schema *yang.Entry
	var cur, new yangtree.DataNode
	_path := gyangtree.ToPath(true, gpath)
	nodes, _ := yangtree.Find(s.Root, _path)
	if len(nodes) == 0 {
		nodes = append(nodes, nil)
		schema = gyangtree.FindSchema(s.RootSchema, gpath)
		if schema.ReadOnly() {
			return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
				"set.replace %q: unable to set read-only data", path)
		}
	}
	for _, cur = range nodes {
		if cur != nil {
			_path = cur.Path()
			schema = gyangtree.FindSchema(s.RootSchema, gpath)
			if schema.ReadOnly() {
				return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
					"set.replace %q: unable to set read-only data", path)
			}
		}

		new, err = gyangtree.New(schema, typedvalue)
		if err != nil {
			return status.TaggedErrorf(codes.InvalidArgument, status.TagBadData,
				"set.replace %q: %v", _path, err)
		}
		if cur == s.Root { // replace new root if it is the root
			s.Root = new
		} else {
			if err := yangtree.Replace(s.Root, _path, new); err != nil {
				return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
					"set.replace %q: %v", _path, err)
			}
		}
		e := &setEntry{op: gnmipb.UpdateResult_REPLACE, path: _path, cur: cur, new: new}
		if err := s.execCallback(e, false); err != nil {
			return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.replace %q: %v", _path, err)
		}
	}
	return nil
}

// setupdate deletes the path from root if the path exists.
func (s *Server) setupdate(prefix, path *gnmipb.Path, typedvalue *gnmipb.TypedValue) error {
	gpath := gyangtree.MergeGNMIPath(prefix, path)
	if glog.V(10) {
		glog.Infof("set.update %q", gyangtree.ToPath(true, gpath))
	}
	if err := gyangtree.ValidateGNMIPath(s.RootSchema, gpath); err != nil {
		return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
			"set.update: invalid path: %v", err)
	}

	var err error
	var schema *yang.Entry
	var cur, new yangtree.DataNode
	_path := gyangtree.ToPath(true, gpath)
	nodes, _ := yangtree.Find(s.Root, _path)
	if len(nodes) == 0 {
		nodes = append(nodes, nil)
		schema = gyangtree.FindSchema(s.RootSchema, gpath)
		if schema.ReadOnly() {
			return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
				"set.update %q: unable to set read-only data", path)
		}
	}
	for _, cur = range nodes {
		if cur != nil {
			_path = cur.Path()
			schema = gyangtree.FindSchema(s.RootSchema, gpath)
			if schema.ReadOnly() {
				return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
					"set.update %q: unable to set read-only data", path)
			}
		}

		new, err = gyangtree.New(schema, typedvalue)
		if err != nil {
			return status.TaggedErrorf(codes.InvalidArgument, status.TagBadData,
				"set.update %q: %v", _path, err)
		}
		backup := yangtree.Clone(cur)
		if err := yangtree.Merge(s.Root, _path, new); err != nil {
			return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.update %q: %v", _path, err)
		}

		e := &setEntry{op: gnmipb.UpdateResult_REPLACE, path: _path, cur: backup, new: new}
		if err := s.execCallback(e, false); err != nil {
			return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.update %q: %v", _path, err)
		}
	}
	return nil
}

// setload loads the startup state of the Server.
// startup is YAML or JSON bytes to populate the data tree.
func (s *Server) setload(startup []byte, encoding Encoding) error {
	new, err := yangtree.New(s.RootSchema)
	if err != nil {
		return status.TaggedError(codes.Internal, status.TagOperationFail, err)
	}
	switch encoding {
	case Encoding_JSON, Encoding_JSON_IETF:
		if err := new.Set(string(startup)); err != nil {
			return status.TaggedError(codes.Internal, status.TagOperationFail, err)
		}
	case Encoding_YAML:
		return status.TaggedErrorf(codes.Unimplemented, status.TagOperationFail,
			"yaml encoding is not yet implemented")
	}
	e := &setEntry{path: "", cur: s.Root, new: new}
	if err := s.execCallback(e, false); err != nil {
		return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
			"load %q:: %v", "", err)
	}
	s.Root = new
	return nil
}

func (s *Server) execCallback(e *setEntry, revert bool) error {
	var cur, new yangtree.DataNode
	if revert {
		cur = e.new
		new = e.cur
	} else {
		cur = e.cur
		new = e.new
		s.setBackup = append(s.setBackup, e)
	}
	// skip e if not executed
	if !e.executed {
		return nil
	}

	if s.setCallback != nil {
		e.executed = true
		return s.setCallback.SetCallback(e.op, e.path, cur, new, false)
	}
	return nil
}
