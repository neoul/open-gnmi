// package for gnmi target implementation
package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"sync"
	"time"

	"github.com/neoul/gtrie"
	"github.com/neoul/open-gnmi/utilities/status"
	"github.com/neoul/yangtree"
	gyangtree "github.com/neoul/yangtree/gnmi"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"

	"github.com/golang/glog"
	"github.com/golang/protobuf/proto"
	"github.com/openconfig/goyang/pkg/yang"

	dpb "github.com/golang/protobuf/protoc-gen-go/descriptor"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
)

// Encoding defines the value encoding formats that are supported by the gNMI server
type Encoding int32

const (
	Encoding_JSON Encoding = 0 // JSON encoded text.
	// Encoding_BYTES     Encoding = 1 // Arbitrarily encoded bytes.
	// Encoding_PROTO     Encoding = 2 // Encoded according to out-of-band agreed Protobuf.
	// Encoding_ASCII     Encoding = 3 // ASCII text of an out-of-band agreed format.
	Encoding_JSON_IETF Encoding = 4 // JSON encoded text as per RFC7951.
	Encoding_YAML      Encoding = 5
)

func (x Encoding) String() string {
	switch x {
	case Encoding_JSON:
		return "json"
	case Encoding_JSON_IETF:
		return "json-ietf"
	case Encoding_YAML:
		return "yaml"
	default:
		return "unknown"
	}
}

var (
	supportedEncodings = []gnmipb.Encoding{gnmipb.Encoding_JSON, gnmipb.Encoding_JSON_IETF}
)

type SetCallback interface {
	SetCallback(op gnmipb.UpdateResult_Operation,
		path string, cur, new yangtree.DataNode, rollback bool) error
}

// Server maintains the device state to provide Capabilities, Get, Set and Subscribe RPCs of gNMI service.
type Server struct {
	*sync.RWMutex
	RootSchema *yang.Entry
	Root       yangtree.DataNode
	Event      *EventCtrl

	reqSeq        uint64
	subSession    map[ObjID]*SubSession
	serverAliases map[string]string // target-defined aliases (server aliases)

	maxSubSession int

	// Fields to request the date updates to the system for Get and Subscribe requests.
	syncMinInterval time.Duration // The minimal interval for SyncCallback
	syncCallback    SyncCallback
	syncEvents      []*syncEvent
	syncPath        *gtrie.Trie

	// For Set
	setDisabled        bool
	setUpdatedByServer bool
	setCallback        SetCallback
	setBackup          []*setEntry
	setSeq             uint64

	Modeldata []*gnmipb.ModelData
}

// Option is an interface used in the gNMI Server configuration
type Option interface {
	// IsOption is a marker method for each Option.
	IsOption()
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

// SetCallbackOption option is used to register SetCallback.
// Upon Set RPC, SetCallback will be invoked by the gNMI Server with the changed data node.
type SetCallbackOption struct {
	SetCallback
	UpdatedByServer bool
}

// IsOption - SetCallbackOption is a Option.
func (o SetCallbackOption) IsOption() {}

func hasSetCallbackFunc(opts []Option) SetCallback {
	for _, o := range opts {
		switch set := o.(type) {
		case SetCallbackOption:
			return set.SetCallback
		}
	}
	return nil
}

func hasUpdatedByServer(opts []Option) bool {
	for _, o := range opts {
		switch set := o.(type) {
		case SetCallbackOption:
			return set.UpdatedByServer
		}
	}
	return true
}

// Aliases (Target-defined aliases configuration within a Subscription)
type Aliases map[string]string

// IsOption - Aliases is a Option.
func (o Aliases) IsOption() {}

func hasAliases(opts []Option) map[string]string {
	for _, o := range opts {
		switch a := o.(type) {
		case Aliases:
			for k, v := range a {
				if glog.V(11) {
					glog.Infof("server-aliases(%s: %s)\n", k, v)
				}
			}
			return map[string]string(a)
		}
	}
	return nil
}

// MaxSubSession is the maximum number of subscription session
type MaxSubSession struct {
	MaxCount int
}

// IsOption - SubMaxSession is a Option
func (o MaxSubSession) IsOption() {}

func hasMaxSubSession(opts []Option) int {
	for _, o := range opts {
		switch v := o.(type) {
		case MaxSubSession:
			return v.MaxCount
		}
	}
	return 16
}

// NewServer creates a new instance of gNMI Server.
//
//  // YANG files to be loaded.
//  files := []string{
// 	 "../../../YangModels/yang/standard/ietf/RFC/iana-if-type@2017-01-19.yang",
// 	 "../../../openconfig/public/release/models/interfaces/openconfig-interfaces.yang",
// 	 "../../../openconfig/public/release/models/interfaces/openconfig-if-ip.yang",
// 	 "../../../openconfig/public/release/models/system/openconfig-messages.yang",
// 	 "../../../openconfig/public/release/models/telemetry/openconfig-telemetry.yang",
// 	 "../../../openconfig/public/release/models/openflow/openconfig-openflow.yang",
// 	 "../../../openconfig/public/release/models/platform/openconfig-platform.yang",
// 	 "../../../openconfig/public/release/models/system/openconfig-system.yang",
// 	 "../../../neoul/yangtree/data/sample/sample.yang",
//  }
//
//  // Directories of YANG files to be imported or includeded
//  dir := []string{"../../../openconfig/public/", "../../../YangModels/yang"}
//
//  // YANG module name to be excluded from the gNMI server model
//  excluded := []string{"ietf-interfaces"}
//
//  // NewServer loads all YANG files and initializes the gNMI server.
//  gnmiserver, err := server.NewServer(*yang, *dir, *excludes, gnmiOptions...)
//  if err != nil {
//  	glog.Exitf("gnmi new server failed: %v", err)
//  }
//  // Registers the gNMI sync-required path.
//  gnmiserver.RegisterSync("/interfaces/interface")
//  // Starts the subsystem.
//  if err := subsystem.Start(gnmiserver); err != nil {
//  	glog.Exitf("start subsystem failed: %v", err)
//  }
//  // Uncomment this for user authentication
//  // opts = append(opts, grpc.UnaryInterceptor(login.UnaryInterceptor))
//  // opts = append(opts, grpc.StreamInterceptor(login.StreamInterceptor))
//  // Creates gRPC server used for the gNMI service.
//  grpcserver := grpc.NewServer(opts...)
//  // Register the gNMI service to the gRPC server.
//  gnmipb.RegisterGNMIServer(grpcserver, gnmiserver)
//  // Creates and binds the socket to the gRPC server to serve the gNMI service.
//  listen, err := net.Listen("tcp", *bindAddr)
//  if err != nil {
//  	glog.Exitf("listen failed: %v", err)
//  }
//  if err := grpcserver.Serve(listen); err != nil {
//  	grpcserver.Stop()
//  	glog.Exitf("serve failed: %v", err)
//  }
func NewServer(file, dir, excluded []string, opts ...Option) (*Server, error) {
	rootschema, err := yangtree.Load(file, dir, excluded, yangtree.SchemaOption{CreatedWithDefault: true})
	if err != nil {
		return nil, err
	}
	root, err := yangtree.New(rootschema)
	if err != nil {
		return nil, err
	}
	s := &Server{
		RWMutex:    &sync.RWMutex{},
		RootSchema: rootschema,
		Root:       root,
		Event:      newEventCtrl(rootschema),

		serverAliases:      hasAliases(opts),
		subSession:         make(map[ObjID]*SubSession),
		maxSubSession:      hasMaxSubSession(opts),
		setDisabled:        hasDisableSetRPC(opts),
		setUpdatedByServer: hasUpdatedByServer(opts),
		setCallback:        hasSetCallbackFunc(opts),
		Modeldata:          gyangtree.GetModuleData(rootschema),
	}
	s.syncInit(opts...)
	return s, nil
}

func (s *Server) AllSchemaPaths() []string {
	entries := yangtree.CollectSchemaEntries(s.RootSchema, true)
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, yangtree.GeneratePath(e, true, false))
	}
	return paths
}

// Load loads the startup state of the Server Model.
// startup is YAML or JSON startup data to populate the creating structure (gostruct).
func (s *Server) Load(startup []byte, encoding Encoding) error {
	s.Lock()
	defer s.Unlock()
	if err := s.setload(startup, encoding); err != nil {
		return err
	}
	s.setdone()
	return nil
}

// CheckEncoding checks whether encoding and models are supported by the server.
// Return error if anything is unsupported.
func (s *Server) CheckEncoding(encoding gnmipb.Encoding) error {
	hasSupportedEncoding := false
	for _, supportedEncoding := range supportedEncodings {
		if encoding == supportedEncoding {
			hasSupportedEncoding = true
			break
		}
	}
	if !hasSupportedEncoding {
		return status.TaggedErrorf(codes.Unimplemented, status.TagNotSupport,
			"unsupported encoding: %s", gnmipb.Encoding_name[int32(encoding)])
	}
	return nil
}

// CheckModels checks whether models are supported by the model. Return error if anything is unsupported.
func (s *Server) CheckModels(models []*gnmipb.ModelData) error {
	if len(models) == 0 {
		return nil
	}
	for _, model := range models {
		isSupported := false
		for _, supportedModel := range s.Modeldata {
			// bugfix - use_models does not behave as defined in gnmi specification.
			// if proto.Equal(model, supportedModel) {
			// 	isSupported = true
			// 	break
			// }
			if model.Name != supportedModel.Name {
				continue
			}
			isSupported = true
			if model.Version != "" && model.Version != supportedModel.Version {
				isSupported = false
			}
			if model.Organization != "" && model.Organization != supportedModel.Organization {
				isSupported = false
			}
			if isSupported {
				break
			}
		}
		if !isSupported {
			if model.Name == "" {
				return status.TaggedErrorf(codes.InvalidArgument,
					status.TagBadData, "invalid supported model data=%v", model)
			}
			return status.TaggedErrorf(codes.Unimplemented,
				status.TagNotSupport, "unsupported model=%v", model)
		}
	}
	return nil
}

// getGNMIServiceVersion returns a pointer to the gNMI service version string.
// The method is non-trivial because of the way it is defined in the proto file.
func getGNMIServiceVersion() (*string, error) {
	gzB, _ := (&gnmipb.Update{}).Descriptor()
	r, err := gzip.NewReader(bytes.NewReader(gzB))
	if err != nil {
		return nil, fmt.Errorf("error in initializing gzip reader: %v", err)
	}
	defer r.Close()
	b, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("error in reading gzip data: %v", err)
	}
	desc := &dpb.FileDescriptorProto{}
	if err := proto.Unmarshal(b, desc); err != nil {
		return nil, fmt.Errorf("error in unmarshaling proto: %v", err)
	}
	ver, err := proto.GetExtension(desc.Options, gnmipb.E_GnmiService)
	if err != nil {
		return nil, fmt.Errorf("error in getting version from proto extension: %v", err)
	}
	return ver.(*string), nil
}

func (s *Server) rpcSequence() uint64 {
	s.reqSeq++
	return s.reqSeq
}

// Capabilities returns supported encodings and supported models.
func (s *Server) Capabilities(ctx context.Context, req *gnmipb.CapabilityRequest) (*gnmipb.CapabilityResponse, error) {
	seq := s.rpcSequence()
	if glog.V(11) {
		glog.Infof("capabilities.request[%d]=%s", seq, req)
	}
	resp, err := s.capabilities(ctx, req)
	if err != nil {
		if glog.V(11) {
			glog.Errorf("capabilities.response[%d]=%v", seq, status.FromError(err))
		}
	} else {
		if glog.V(11) {
			glog.Infof("capabilities.response[%d]=%s", seq, resp)
		}
	}
	return resp, err
}

// Get implements the Get RPC in gNMI spec.
func (s *Server) Get(ctx context.Context, req *gnmipb.GetRequest) (*gnmipb.GetResponse, error) {
	seq := s.rpcSequence()
	if glog.V(11) {
		glog.Infof("get.request[%d]=%v", seq, req)
	}
	resp, err := s.get(ctx, req)
	if err != nil {
		if glog.V(11) {
			glog.Errorf("get.response[%d]=%v", seq, status.FromError(err))
		}
	} else {
		if glog.V(11) {
			glog.Infof("get.response[%d]=%v", seq, resp)
		}
	}
	return resp, err
}

// Set implements the Set RPC in gNMI spec.
func (s *Server) Set(ctx context.Context, req *gnmipb.SetRequest) (*gnmipb.SetResponse, error) {
	seq := s.rpcSequence()
	if glog.V(11) {
		glog.Infof("set.request[%d]=%s", seq, req)
	}
	if s.setDisabled {
		err := status.TaggedErrorf(codes.Unimplemented, status.TagNotSupport, "set not supported")
		if glog.V(11) {
			glog.Errorf("set.response[%d]=%v", seq, status.FromError(err))
		}
		return nil, err
	}
	resp, err := s.set(ctx, req)
	if err != nil {
		if glog.V(11) {
			glog.Errorf("set.response[%d]=%v", seq, status.FromError(err))
		}
	} else {
		if glog.V(11) {
			glog.Infof("set.response[%d]=%s", seq, resp)
		}
	}
	return resp, err
}

// Subscribe implements the Subscribe RPC in gNMI spec.
func (s *Server) Subscribe(stream gnmipb.GNMI_SubscribeServer) error {
	subses, err := s.startSubSession(stream.Context())
	if err != nil {
		return err
	}

	// run stream responsor
	subses.waitgroup.Add(1)
	go func() {
		defer subses.waitgroup.Done()
		for {
			select {
			case <-subses.shutdown:
				return
			case resp, ok := <-subses.respchan.Channel():
				if ok {
					stream.Send(resp)
					if glog.V(11) {
						glog.Infof("subscribe[%s:%d:%d].response=%s",
							subses.Address, subses.Port, subses.ID, resp)
					}
				} else {
					return
				}
			}
		}
	}()
	err = s.subscribe(subses, stream)
	subses.Stop()
	return err
}

// Capabilities returns supported encodings and models.
func (s *Server) capabilities(ctx context.Context, req *gnmipb.CapabilityRequest) (*gnmipb.CapabilityResponse, error) {
	ver, err := getGNMIServiceVersion()
	if err != nil {
		return nil, status.TaggedErrorf(codes.Internal, status.TagOperationFail,
			"gnmi service version error: %v", err)
	}
	return &gnmipb.CapabilityResponse{
		SupportedModels:    s.Modeldata,
		SupportedEncodings: supportedEncodings,
		GNMIVersion:        *ver,
	}, nil
}

// Get returns the GetResponse for the GetRequest.
// Error in Get RPC
//  - Invalid path: InvalidArgument if the path format is invalid or the schema node of the path is not found
//  - [FIXME] type[CONFIG, STATE, OPERATIONAL]: Unimplemented if set (Not supported feature)
//  - Encoding: Unimplemented if unsupported encoding
//  - uses_models: InvalidArgument if the used model name is an empty string, Unimplemented if the model is not supported
//  - No data instance: NotFound if the data doesn’t exist
func (s *Server) get(ctx context.Context, req *gnmipb.GetRequest) (*gnmipb.GetResponse, error) {
	// A notification {prefix, update[]} per requested path
	// GetResponse must have N notifications for N requested path.
	if req.GetType() != gnmipb.GetRequest_ALL {
		return nil, status.TaggedErrorf(codes.Unimplemented, status.TagNotSupport,
			"unsupported request type: %s", gnmipb.GetRequest_DataType_name[int32(gnmipb.GetRequest_ALL)])
	}
	if err := s.CheckModels(req.GetUseModels()); err != nil {
		return nil, err
	}
	if err := s.CheckEncoding(req.GetEncoding()); err != nil {
		return nil, err
	}

	prefix, paths := req.GetPrefix(), req.GetPath()
	if len(paths) == 0 {
		return nil, status.TaggedErrorf(codes.InvalidArgument,
			status.TagMissingPath, "no path requested")
	}

	s.syncRequest(prefix, paths)

	s.RLock()
	defer s.RUnlock()

	// each prefix + path ==> one notification message
	if err := gyangtree.ValidateGNMIPath(s.RootSchema, prefix); err != nil {
		return nil, status.TaggedError(codes.InvalidArgument, status.TagInvalidPath, err)
	}
	var rerr error
	branches, err := gyangtree.Find(s.Root, prefix)
	// Get RPC will be failed if no data exists.
	if err != nil || len(branches) <= 0 {
		return nil, status.TaggedErrorf(codes.NotFound, status.TagDataMissing,
			"data not found from %v", gyangtree.ToPath(true, prefix))
	}

	notifications := make([]*gnmipb.Notification, 0, len(branches)*len(paths))
	for _, branch := range branches {
		bpath := branch.Path()
		bprefix, err := gyangtree.ToGNMIPath(bpath)
		if err != nil {
			return nil, status.TaggedErrorf(codes.Internal, status.TagInvalidPath,
				"path converting error for %s", bpath)
		}
		for _, path := range paths {
			if err := gyangtree.ValidateGNMIPath(branch.Schema(), path); err != nil {
				return nil, status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
					"invalid path: %s", err)
			}
			datalist, err := gyangtree.Find(branch, path)
			if err != nil || len(datalist) <= 0 {
				rerr = status.TaggedErrorf(codes.NotFound, status.TagDataMissing,
					"data not found from %v", gyangtree.ToPath(true, prefix, path))
				continue
			}
			update := make([]*gnmipb.Update, 0, len(datalist))
			for _, data := range datalist {
				typedValue, err := gyangtree.DataNodeToTypedValue(data, req.GetEncoding())
				if err != nil {
					return nil, status.TaggedErrorf(codes.Internal, status.TagBadData,
						"typed-value encoding error in %s: %v", data.Path(), err)
				}
				datapath, err := gyangtree.ToGNMIPath(data.Path())
				if err != nil {
					return nil, status.TaggedErrorf(codes.Internal, status.TagInvalidPath,
						"path converting error for %s", data.Path())
				}
				gyangtree.UpdateGNMIPath(datapath, path)
				update = append(update, &gnmipb.Update{Path: datapath, Val: typedValue})
			}
			if len(update) == 0 {
				rerr = status.TaggedErrorf(codes.NotFound, status.TagDataMissing,
					"data not found from %v", gyangtree.ToPath(true, prefix, path))
				continue
			}
			gyangtree.UpdateGNMIPath(bprefix, prefix)
			notification := &gnmipb.Notification{
				Timestamp: time.Now().UnixNano(),
				Prefix:    bprefix,
				Update:    update,
			}
			notifications = append(notifications, notification)
		}
	}
	// Get RPC will be failed if no data exists.
	if len(notifications) == 0 {
		if rerr != nil {
			return nil, rerr
		}
		return nil, status.TaggedErrorf(codes.NotFound, status.TagDataMissing, "data not found for required paths")
	}
	return &gnmipb.GetResponse{Notification: notifications}, nil
}

// Set implements the Set RPC in gNMI spec.
func (s *Server) set(ctx context.Context, req *gnmipb.SetRequest) (*gnmipb.SetResponse, error) {
	var err error
	prefix := req.GetPrefix()
	result := make([]*gnmipb.UpdateResult, 0,
		len(req.GetDelete())+len(req.GetReplace())+len(req.GetUpdate())+1)

	// Lock updates of the data node
	s.Lock()
	defer s.Unlock()

	s.setinit()
	for _, path := range req.GetDelete() {
		if err != nil {
			result = append(result, buildUpdateResultAborted(gnmipb.UpdateResult_DELETE, path))
			continue
		}
		err = s.setdelete(prefix, path)
		result = append(result, buildUpdateResult(gnmipb.UpdateResult_DELETE, path, err))
	}
	for _, r := range req.GetReplace() {
		path := r.GetPath()
		if err != nil {
			result = append(result, buildUpdateResultAborted(gnmipb.UpdateResult_REPLACE, path))
			continue
		}
		err = s.setreplace(prefix, path, r.GetVal())
		result = append(result, buildUpdateResult(gnmipb.UpdateResult_REPLACE, path, err))
	}
	for _, u := range req.GetUpdate() {
		path := u.GetPath()
		if err != nil {
			result = append(result, buildUpdateResultAborted(gnmipb.UpdateResult_UPDATE, path))
			continue
		}
		err = s.setupdate(prefix, path, u.GetVal())
		result = append(result, buildUpdateResult(gnmipb.UpdateResult_UPDATE, path, err))
	}
	if err != nil {
		s.setrollback()
	}
	s.setdone()
	resp := &gnmipb.SetResponse{
		Prefix:   prefix,
		Response: result,
	}
	return resp, err
}

// Subscribe implements the Subscribe RPC in gNMI spec.
func (s *Server) subscribe(subses *SubSession, stream gnmipb.GNMI_SubscribeServer) error {
	for {
		req, err := stream.Recv()
		seq := s.rpcSequence()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			if glog.V(11) {
				glog.Infof("subscribe[%s:%d:%d].request[%d]=%v",
					subses.Address, subses.Port, subses.ID, seq, err)
			}
			return err
		}
		if glog.V(11) {
			glog.Infof("subscribe[%s:%d:%d].request[%d]=%v",
				subses.Address, subses.Port, subses.ID, seq, req)
			// proto.MarshalTextString(req)
		}
		if err = subses.processSubscribeRequest(req); err != nil {
			if glog.V(11) {
				glog.Errorf("subscribe[%s:%d:%d].response[%d]=%v",
					subses.Address, subses.Port, subses.ID, seq, err)
			}
			return err
		}
	}
}
