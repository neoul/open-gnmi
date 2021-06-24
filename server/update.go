package server

import (
	"github.com/golang/glog"
	"github.com/neoul/open-gnmi/utilities/status"
	"github.com/neoul/yangtree"
	"google.golang.org/grpc/codes"
)

func (s *Server) Write(path string, value ...string) error {
	s.Lock()
	defer s.Unlock()
	c, r, err := yangtree.SetDiff(s.Root, path, value...)
	if err != nil {
		err := status.TaggedErrorf(codes.Internal,
			status.TagOperationFail, "write %s: %v", path, err)
		if glog.V(10) {
			glog.Error(err)
		}
		return err
	}
	if err := s.Event.SetEvent(c, r, nil); err != nil {
		err := status.TaggedErrorf(codes.Internal,
			status.TagOperationFail, "write %s: %v", path, err)
		if glog.V(10) {
			glog.Error(err)
		}
		return err
	}
	return nil
}

func (s *Server) Delete(path string) error {
	s.Lock()
	defer s.Unlock()
	node, err := yangtree.Find(s.Root, path)
	if err != nil {
		err := status.TaggedErrorf(codes.Internal,
			status.TagOperationFail, "delete %s: %v", path, err)
		if glog.V(10) {
			glog.Error(err)
		}
		return err
	}
	for i := range node {
		deleted, err := yangtree.Find(node[i], "...")
		if err != nil {
			err = status.TaggedErrorf(codes.Internal,
				status.TagOperationFail, "delete %s: %v", path, err)
			if glog.V(10) {
				glog.Error(err)
			}
		}
		if err := s.Event.SetEvent(nil, nil, deleted); err != nil {
			err := status.TaggedErrorf(codes.Internal,
				status.TagOperationFail, "delete %s: %v", path, err)
			if glog.V(10) {
				glog.Error(err)
			}
			return err
		}
		if err = node[i].Remove(); err != nil {
			err := status.TaggedErrorf(codes.Internal,
				status.TagOperationFail, "delete %s: %v", path, err)
			if glog.V(10) {
				glog.Error(err)
			}
			return err
		}
	}
	return nil
}
