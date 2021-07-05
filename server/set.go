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

type SetCallback interface {
	SetCallback(op gnmipb.UpdateResult_Operation,
		path string, cur, new yangtree.DataNode) error
}

type SetTransaction interface {
	SetStart(setID uint, rollback bool) error
	SetEnd(setID uint) error
}

// DisableSetRPC option is used to disable Set RPC
type DisableSetRPC struct {
}

// IsOption - DisableSetRPC is a Option.
func (o DisableSetRPC) IsOption() {}

func hasDisableSetRPC(opts []Option) bool {
	for _, o := range opts {
		switch o.(type) {
		case DisableSetRPC:
			return true
		}
	}
	return false
}

// SetCallbackOption is an gNMI server option used to register SetCallback.
// Upon Set RPC, SetCallback will be invoked by the gNMI Server with the changed data node.
type SetCallbackOption struct {
	SetCallback
	SetTransaction
	SetUpdatedByServer bool
}

// IsOption - SetCallbackOption is a Option.
func (o SetCallbackOption) IsOption() {}

func hasSetCallback(opts []Option) SetCallback {
	for _, o := range opts {
		switch set := o.(type) {
		case SetCallbackOption:
			return set.SetCallback
		}
	}
	return nil
}

func hasSetTransaction(opts []Option) SetTransaction {
	for _, o := range opts {
		switch set := o.(type) {
		case SetCallbackOption:
			return set.SetTransaction
		}
	}
	return nil
}

func hasUpdatedByServer(opts []Option) bool {
	for _, o := range opts {
		switch set := o.(type) {
		case SetCallbackOption:
			return set.SetUpdatedByServer
		}
	}
	return true
}

type setEntry struct {
	Operation gnmipb.UpdateResult_Operation
	Path      string
	Cur       yangtree.DataNode
	New       yangtree.DataNode
	executed  bool
}

func (s *Server) setInit(opts ...Option) error {
	s.setDisabled = hasDisableSetRPC(opts)
	s.setUpdatedByServer = hasUpdatedByServer(opts)
	s.setTransaction = hasSetTransaction(opts)
	s.setCallback = hasSetCallback(opts)
	return nil
}

// setStart initializes the Set transaction.
func (s *Server) setStart(rollback bool) error {
	if !rollback {
		s.setSeq++
		s.setBackup = nil
	}
	if s.setTransaction != nil {
		err := s.setTransaction.SetStart(s.setSeq, rollback)
		if err != nil {
			if glog.V(10) {
				glog.Error(status.TaggedErrorf(codes.Internal, status.TagOperationFail,
					"set.start error: %v", err))
			}
		}
		return err
	}
	return nil
}

// setEnd ends the Set transaction.
func (s *Server) setEnd(err error) error {
	if s.setTransaction == nil {
		return err
	}
	enderr := s.setTransaction.SetEnd(s.setSeq)
	if enderr != nil {
		if glog.V(10) {
			glog.Error(status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.end error: %v", enderr))
		}
	}
	if err != nil {
		return err
	}
	return enderr
}

// setDone clears the rollback data and resets all configuration in order to receive updates from the system.
func (s *Server) setDone(err error) {
	if !s.setUpdatedByServer || err != nil {
		// The set data is reverted to the previous data if the setUpdatedByServer is disabled.
		// The set data must be updated by the system, not the gnmi server.
		for _, e := range s.setBackup {
			if glog.V(10) {
				glog.Infof("set.done %s,%q", e.Operation, e.Path)
			}
			if e.Path == "" || e.Path == "/" { // root
				s.Root = e.Cur
			} else {
				err := yangtree.Replace(s.Root, e.Path, e.Cur)
				if err != nil {
					fmt.Println(err)
					if glog.V(10) {
						glog.Error(status.TaggedErrorf(codes.Internal, status.TagOperationFail,
							"set.done %s: %v", e.Path, err))
					}
				}
			}
		}
	}
	s.setBackup = nil
	if glog.V(10) {
		glog.Infof("set.done")
	}
}

// setRollback executes setIndirect to revert the configuration.
func (s *Server) setRollback() {
	if s.setTransaction == nil {
		return
	}
	for _, e := range s.setBackup {
		if glog.V(10) {
			glog.Infof("set.rollback %q", e.Path)
		}
		// error is ignored on rollback
		if err := s.execSetCallback(e, true); err != nil {
			if glog.V(10) {
				glog.Error(status.TaggedErrorf(codes.Internal, status.TagOperationFail,
					"set.rollback %q: %v", e.Path, err))
			}
		}
	}
}

// setDelete deletes the data nodes of the path.
// 3.4.6 Deleting Configuration
//  - All descendants of the target path are recursively deleted.
//  - Wildcards (*, ...) are supported.
//  - Non-existent node deletion is accepted.
func (s *Server) setDelete(prefix, path *gnmipb.Path) error {
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
		e := &setEntry{Operation: gnmipb.UpdateResult_DELETE, Path: path, Cur: node, New: nil}
		if err := s.execSetCallback(e, false); err != nil {
			return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.delete %q: %v", path, err)
		}
	}
	return nil
}

// setReplace deletes the path from root if the path exists.
// 3.4.4 Modes of Update: Replace versus Update
//  1. converts the typed value to new data node with the default value.
//  2. replace the cur data node to the new data node.
func (s *Server) setReplace(prefix, path *gnmipb.Path, typedvalue *gnmipb.TypedValue) error {
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
			if new, err = gyangtree.Replace(s.Root, _path, new); err != nil {
				return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
					"set.replace %q: %v", _path, err)
			}
		}
		e := &setEntry{Operation: gnmipb.UpdateResult_REPLACE, Path: new.Path(), Cur: cur, New: new}
		if err := s.execSetCallback(e, false); err != nil {
			return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.replace %q: %v", new.Path(), err)
		}
	}
	return nil
}

// setUpdate deletes the path from root if the path exists.
func (s *Server) setUpdate(prefix, path *gnmipb.Path, typedvalue *gnmipb.TypedValue) error {
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
		if new, err = gyangtree.Update(s.Root, _path, new); err != nil {
			return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.update %q: %v", _path, err)
		}

		e := &setEntry{Operation: gnmipb.UpdateResult_REPLACE, Path: new.Path(), Cur: backup, New: new}
		if err := s.execSetCallback(e, false); err != nil {
			return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
				"set.update %q: %v", new.Path(), err)
		}
	}
	return nil
}

// setLoad loads the startup state of the Server.
// startup is YAML or JSON bytes to populate the data tree.
func (s *Server) setLoad(startup []byte, encoding Encoding) error {
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
	e := &setEntry{Path: "/", Cur: s.Root, New: new}
	if err := s.execSetCallback(e, false); err != nil {
		return status.TaggedErrorf(codes.Internal, status.TagOperationFail,
			"load %q:: %v", "", err)
	}
	s.Root = new
	return nil
}

func (s *Server) execSetCallback(e *setEntry, rollback bool) error {
	var cur, new yangtree.DataNode
	if !rollback {
		cur = e.Cur
		new = e.New
		s.setBackup = append(s.setBackup, e)
	} else {
		cur = e.New
		new = e.Cur
		if !e.executed {
			return nil
		}
	}
	if s.setCallback == nil {
		return nil
	}
	e.executed = true
	return s.setCallback.SetCallback(e.Operation, e.Path, cur, new)
}
