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
	setTransaction     SetTransaction
	setBackup          []*setEntry
	setSeq             uint

	Modeldata []*gnmipb.ModelData
}

// Option is an interface used in the gNMI Server configuration
type Option interface {
	// IsOption is a marker method for each Option.
	IsOption()
}

// Aliases (Target-defined aliases; server aliases) configuration
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
			return (map[string]string)(a)
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
//   "../yang/openconfig-telemetry-dev.yang",
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

		serverAliases: hasAliases(opts),
		subSession:    make(map[ObjID]*SubSession),
		maxSubSession: hasMaxSubSession(opts),
		Modeldata:     gyangtree.GetModuleData(rootschema),
	}
	s.setInit(opts...)
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
	err := s.setStart(false)
	if err == nil {
		err = s.setLoad(startup, encoding)
	}
	err = s.setEnd(err)
	if err != nil {
		s.setStart(true)
		s.setRollback()
		s.setEnd(nil)
	}
	s.setDone(err)
	return err
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

// GetGNMIVersion returns a pointer to the gNMI service version string.
// The method is non-trivial because of the way it is defined in the proto file.
func GetGNMIVersion() (*string, error) {
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
	ver, err := GetGNMIVersion()
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
	if err := s.CheckModels(req.GetUseModels()); err != nil {
		return nil, err
	}
	if err := s.CheckEncoding(req.GetEncoding()); err != nil {
		return nil, err
	}

	gprefix, gpath := req.GetPrefix(), req.GetPath()
	sprefix, spath, err := gyangtree.ValidateAndConvertGNMIPath(s.RootSchema, gprefix, gpath)
	if err != nil {
		return nil, status.TaggedError(codes.InvalidArgument, status.TagInvalidPath, err)
	}
	// request data updates for the paths.
	for i := range spath {
		s.SyncRequest(yangtree.FindAllPossiblePath(s.RootSchema, sprefix+spath[i]))
	}

	s.RLock()
	defer s.RUnlock()

	var findopt []yangtree.Option
	switch req.GetType() {
	case gnmipb.GetRequest_CONFIG:
		findopt = append(findopt, yangtree.ConfigOnly{})
	case gnmipb.GetRequest_OPERATIONAL:
		return nil, status.TaggedErrorf(codes.Unimplemented, status.TagNotSupport,
			"unsupported request type: %s", gnmipb.GetRequest_DataType_name[int32(gnmipb.GetRequest_ALL)])
	case gnmipb.GetRequest_STATE:
		findopt = append(findopt, yangtree.StateOnly{})
	}

	branches, err := yangtree.Find(s.Root, sprefix, findopt...)
	if err != nil { // finding error
		return nil, status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidPath,
			"error in finding: %v", err)
	}
	if len(branches) <= 0 { // get failed if no data exists.
		return nil, status.TaggedErrorf(codes.NotFound, status.TagDataMissing,
			"data not found from %v", sprefix)
	}

	var rerr error
	notifications := make([]*gnmipb.Notification, 0, len(branches)*len(spath))
	for _, branch := range branches {
		for i := range spath {
			node, err := yangtree.Find(branch, spath[i], findopt...)
			if err != nil || len(node) <= 0 {
				rerr = status.TaggedErrorf(codes.NotFound, status.TagDataMissing,
					"data not found from %v", sprefix)
				continue
			}

			update := make([]*gnmipb.Update, 0, len(node))
			for _, data := range node {
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
				update = append(update, &gnmipb.Update{Path: datapath, Val: typedValue})
			}
			notification := &gnmipb.Notification{
				Timestamp: time.Now().UnixNano(),
				Prefix:    gprefix,
				Update:    update,
			}
			notifications = append(notifications, notification)
		}
	}
	// get failed if no data exists.
	if len(notifications) == 0 {
		if rerr != nil {
			return nil, rerr
		}
		return nil, status.TaggedErrorf(codes.NotFound, status.TagDataMissing, "data not found for the requested paths")
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

	err = s.setStart(false)
	for _, path := range req.GetDelete() {
		if err != nil {
			result = append(result, buildUpdateResultAborted(gnmipb.UpdateResult_DELETE, path))
			continue
		}
		err = s.setDelete(prefix, path)
		result = append(result, buildUpdateResult(gnmipb.UpdateResult_DELETE, path, err))
	}
	for _, r := range req.GetReplace() {
		path := r.GetPath()
		if err != nil {
			result = append(result, buildUpdateResultAborted(gnmipb.UpdateResult_REPLACE, path))
			continue
		}
		err = s.setReplace(prefix, path, r.GetVal())
		result = append(result, buildUpdateResult(gnmipb.UpdateResult_REPLACE, path, err))
	}
	for _, u := range req.GetUpdate() {
		path := u.GetPath()
		if err != nil {
			result = append(result, buildUpdateResultAborted(gnmipb.UpdateResult_UPDATE, path))
			continue
		}
		err = s.setUpdate(prefix, path, u.GetVal())
		result = append(result, buildUpdateResult(gnmipb.UpdateResult_UPDATE, path, err))
	}
	err = s.setEnd(err)
	if err != nil {
		s.setStart(true)
		s.setRollback()
		s.setEnd(nil)
	}
	s.setDone(err)
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
