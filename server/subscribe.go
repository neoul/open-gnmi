package server

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/neoul/gtrie"
	"github.com/neoul/open-gnmi/utilities"
	"github.com/neoul/open-gnmi/utilities/status"
	"github.com/neoul/yangtree"
	gyangtree "github.com/neoul/yangtree/gnmi"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
)

// ObjID - gnmi server object ID
type ObjID uint64

type subscribeResponseChannel interface {
	Channel() chan *gnmipb.SubscribeResponse
	Send(sponses []*gnmipb.SubscribeResponse) error
	Close()
}

type dialinChannel struct {
	channel chan *gnmipb.SubscribeResponse
}

func (dialin *dialinChannel) Send(responses []*gnmipb.SubscribeResponse) error {
	if dialin == nil || dialin.channel == nil {
		return fmt.Errorf("subscribe-reponse channel closed")
	}
	for _, response := range responses {
		dialin.channel <- response
	}
	return nil
}

func (dialin *dialinChannel) Close() {
	if dialin != nil && dialin.channel != nil {
		close(dialin.channel)
		dialin.channel = nil
	}
}

func (dialin *dialinChannel) Channel() chan *gnmipb.SubscribeResponse {
	if dialin != nil {
		return dialin.channel
	}
	return nil
}

// SubSession - gNMI gRPC Subscribe RPC (Telemetry) session information managed by server
type SubSession struct {
	ID        ObjID
	Address   string
	Port      uint16
	Sub       map[string]*Subscriber
	respchan  subscribeResponseChannel
	shutdown  chan struct{}
	waitgroup *sync.WaitGroup
	caliases  *clientAliases
	*Server
}

var (
	sessID ObjID
	subID  ObjID
)

func (subses *SubSession) String() string {
	return fmt.Sprintf("%s:%d", subses.Address, subses.Port)
}

func (s *Server) startSubSession(ctx context.Context) (*SubSession, error) {
	s.Lock()
	defer s.Unlock()
	var address string
	var port int
	sessID++
	_, remoteaddr, _ := utilities.QueryAddr(ctx)
	addr := remoteaddr.String()
	end := strings.LastIndex(addr, ":")
	if end >= 0 {
		address = addr[:end]
		port, _ = strconv.Atoi(addr[end+1:])
	}
	if len(s.subSession) > s.maxSubSession {
		err := status.TaggedErrorf(codes.OutOfRange, status.TagOperationFail,
			"the maximum number of subscriptions exceeded (must be <=%d)", s.maxSubSession)
		if glog.V(11) {
			glog.Errorf("subscribe[%s:%d:%d].response:: %v", address, uint16(port), sessID, status.FromError(err))
		}
		return nil, err
	}
	if glog.V(11) {
		glog.Infof("subscribe[%s:%d:%d].start", address, uint16(port), sessID)
	}
	subses := &SubSession{
		ID:        sessID,
		Address:   address,
		Port:      uint16(port),
		Sub:       map[string]*Subscriber{},
		respchan:  &dialinChannel{channel: make(chan *gnmipb.SubscribeResponse, 32)},
		shutdown:  make(chan struct{}),
		waitgroup: new(sync.WaitGroup),
		caliases:  newClientAliases(s.RootSchema),
		Server:    s,
	}
	s.subSession[subses.ID] = subses
	return subses, nil
}

func (subses *SubSession) Stop() {
	// shutdown all goroutines for subscription
	if glog.V(11) {
		glog.Infof("subscribe[%s:%d:%d].stop:: signaling shutdown",
			subses.Address, subses.Port, subses.ID)
	}
	close(subses.shutdown)
	subses.waitgroup.Wait()
	if glog.V(11) {
		glog.Infof("subscribe[%s:%d:%d].stop:: stopped goroutines",
			subses.Address, subses.Port, subses.ID)
	}

	// clear stream subscription
	for key, sub := range subses.Sub {
		subses.Server.Event.Unregister(sub)
		subses.deleteDynamicSubscriptionInfo(sub)
		subses.deleteSubscription(sub)
		delete(subses.Sub, key)
	}
	subses.Sub = nil

	// clear response channel
	if subses.respchan != nil {
		subses.respchan.Close()
		subses.respchan = (*dialinChannel)(nil)
	}

	// clear aliases
	clearClientAliases(subses.caliases)
	subses.caliases = nil
	if glog.V(11) {
		glog.Infof("subscribe[%s:%d:%d].stop:: closed subsession",
			subses.Address, subses.Port, subses.ID)
	}
	subses.Server.Lock()
	delete(subses.Server.subSession, subses.ID)
	subses.Server.Unlock()
}

type Duplicates struct {
	Count uint32
}

type changeEvent struct {
	changes yangtree.DataNode
	updated *gtrie.Trie
	deleted *gtrie.Trie
}

func (event *changeEvent) Clear() {
	if event == nil {
		return
	}
	event.updated.Clear()
	event.deleted.Clear()
	event.changes = nil
}

// Subscriber - Telemetry Subscription structure for Telemetry Update
type Subscriber struct {
	ID                ObjID
	SessionID         ObjID
	key               string
	UseAliases        bool                         `json:"use_aliases,omitempty"`
	Mode              gnmipb.SubscriptionList_Mode `json:"stream_mode,omitempty"`
	AllowAggregation  bool                         `json:"allow_aggregation,omitempty"`
	Encoding          gnmipb.Encoding              `json:"encoding,omitempty"`
	Prefix            string                       `json:"prefix,omitempty"`            // string prefix subscribed
	Path              []string                     `json:"path,omitempty"`              // string path subscribed
	StreamMode        gnmipb.SubscriptionMode      `json:"subscription_mode,omitempty"` // Subscription mode to be used.
	SampleInterval    uint64                       `json:"sample_interval,omitempty"`   // ns between samples in SAMPLE mode.
	SuppressRedundant bool                         `json:"suppress_redundant,omitempty"`
	HeartbeatInterval uint64                       `json:"heartbeat_interval,omitempty"`

	StreamConfig struct {
		StreamMode        gnmipb.SubscriptionMode
		SampleInterval    uint64
		SuppressRedundant bool
		HeartbeatInterval uint64
	}

	// internal data
	fullpath []string
	GPrefix  *gnmipb.Path
	session  *SubSession
	onchange chan *changeEvent // event channel for on-change event
	pending  *changeEvent      // event pending
	started  bool
	mutex    *sync.Mutex
}

// EventReceiver interface for Telemetry subscription
func (subscriber *Subscriber) EventStart(uint) {}

// EventReceiver interface for Telemetry subscription
func (subscriber *Subscriber) EventComplete(eid uint) {
	switch subscriber.StreamMode {
	case gnmipb.SubscriptionMode_ON_CHANGE:
		if subscriber.onchange != nil {
			if subscriber.pending != nil {
				if glog.V(11) {
					subses := subscriber.session
					glog.Infof("telemetry.event sent to subscribe[%s:%d:%d].stream[%d]",
						subses.Address, subses.Port, subses.ID, subscriber.ID)
				}
				subscriber.onchange <- subscriber.pending
			}
		}
		subscriber.pending = nil
	}
}

// EventReceiver interface for Telemetry subscription
func (subscriber *Subscriber) EventReceive(eid uint, event EventType, path string) {
	var edata *changeEvent
	if subscriber.pending == nil {
		edata = &changeEvent{
			updated: gtrie.New(),
			deleted: gtrie.New(),
		}
	} else {
		edata = subscriber.pending
	}
	switch subscriber.StreamMode {
	case gnmipb.SubscriptionMode_ON_CHANGE:
		if edata.changes == nil {
			edata.changes, _ =
				yangtree.New(subscriber.session.RootSchema)
		}
		if event == EventCreate || event == EventReplace {
			node, _ := yangtree.Find(subscriber.session.Root, path)
			for i := range node {
				yangtree.Merge(edata.changes, path, node[i])
			}
		}
	}

	switch event {
	case EventCreate, EventReplace:
		if v, ok := edata.updated.Find(path); !ok {
			edata.updated.Add(path, &Duplicates{Count: 1})
		} else {
			v.(*Duplicates).Count++
		}
	}
	switch event {
	case EventReplace, EventDelete:
		if v, ok := edata.deleted.Find(path); !ok {
			edata.deleted.Add(path, &Duplicates{Count: 1})
		} else {
			v.(*Duplicates).Count++
		}
	}
	subscriber.pending = edata
}

// EventReceiver interface for Telemetry subscription
func (subscriber *Subscriber) EventPath() []string {
	return subscriber.fullpath
}

func (subscriber *Subscriber) String() string {
	return subscriber.key
}

func (subscriber *Subscriber) run() {
	var subses *SubSession = subscriber.session
	var samplingTimer *time.Ticker
	var heartbeatTimer *time.Ticker
	expired := make(chan bool, 2)
	if subscriber.StreamConfig.SampleInterval > 0 {
		tick := time.Duration(subscriber.StreamConfig.SampleInterval)
		samplingTimer = time.NewTicker(tick * time.Nanosecond)
	} else {
		tick := time.Duration(defaultInterval)
		samplingTimer = time.NewTicker(tick * time.Nanosecond)
		samplingTimer.Stop() // stop
	}
	if subscriber.StreamConfig.HeartbeatInterval > 0 {
		tick := time.Duration(subscriber.StreamConfig.HeartbeatInterval)
		heartbeatTimer = time.NewTicker(tick * time.Nanosecond)
	} else {
		tick := time.Duration(defaultInterval)
		heartbeatTimer = time.NewTicker(tick * time.Nanosecond)
		heartbeatTimer.Stop() // stop
	}
	defer func() {
		if glog.V(11) {
			glog.Warningf("subscribe[%s:%d:%d].stream[%d]:: terminated",
				subses.Address, subses.Port, subses.ID, subscriber.ID)
		}
		subscriber.started = false
		samplingTimer.Stop()
		heartbeatTimer.Stop()
		subses.waitgroup.Done()
		close(expired)
	}()
	if samplingTimer == nil || heartbeatTimer == nil {
		if glog.V(11) {
			glog.Errorf("subscribe[%s:%d:%d].stream[%d]:: nil timer",
				subses.Address, subses.Port, subses.ID, subscriber.ID)
		}
		return
	}

	for {
		select {
		case <-subses.shutdown:
			if glog.V(11) {
				glog.Infof("subscribe[%s:%d:%d].stream[%d]:: shutdown",
					subses.Address, subses.Port, subses.ID, subscriber.ID)
			}
			return
		case event, ok := <-subscriber.onchange:
			if !ok {
				if glog.V(11) {
					glog.Errorf("subscribe[%s:%d:%d].stream[%d]:: event-queue closed",
						subses.Address, subses.Port, subses.ID, subscriber.ID)
				}
				return
			}
			if glog.V(11) {
				glog.Infof("subscribe[%s:%d:%d].stream[%d]:: event received",
					subses.Address, subses.Port, subses.ID, subscriber.ID)
			}
			switch subscriber.StreamConfig.StreamMode {
			case gnmipb.SubscriptionMode_ON_CHANGE:
				err := subses.telemetryUpdate(subscriber, event)
				if err != nil {
					if glog.V(11) {
						glog.Errorf("subscribe[%s:%d:%d].stream[%d]:: error: %v",
							subses.Address, subses.Port, subses.ID, subscriber.ID, err)
					}
					return
				}
			default:
			}
			event.Clear()
		case <-samplingTimer.C:
			if glog.V(11) {
				glog.Infof("subscribe[%s:%d:%d].stream[%d]:: sampling-timer expired",
					subses.Address, subses.Port, subses.ID, subscriber.ID)
			}
			subscriber.mutex.Lock()
			subscriber.session.SyncRequest(subscriber.fullpath)
			subscriber.mutex.Unlock()
			expired <- !subscriber.StreamConfig.SuppressRedundant
		case <-heartbeatTimer.C:
			if glog.V(11) {
				glog.Infof("subscribe[%s:%d:%d].stream[%d]:: heartbeat-timer expired",
					subses.Address, subses.Port, subses.ID, subscriber.ID)
			}
			expired <- true
		case mustSend := <-expired:
			event := subscriber.pending
			if !mustSend && event != nil {
				// suppress_redundant - skips the telemetry update if no changes
				if event.updated.Size() > 0 ||
					event.deleted.Size() > 0 {
					mustSend = true
				}
			}
			if mustSend {
				if glog.V(11) {
					glog.Infof("subscribe[%s:%d:%d].stream[%d]:: try to send updates",
						subses.Address, subses.Port, subses.ID, subscriber.ID)
				}
				subscriber.mutex.Lock() // block to modify subscriber.Path
				err := subses.telemetryUpdate(subscriber, event)
				subscriber.mutex.Unlock()
				if err != nil {
					if glog.V(11) {
						glog.Errorf("subscribe[%s:%d:%d].stream[%d]:: error: %v",
							subses.Address, subses.Port, subses.ID, subscriber.ID, err)
					}
					return
				}
				event.Clear()
			}
		}
	}
}

func getDeletes(path string, deleteOnly bool, event *changeEvent) ([]*gnmipb.Path, error) {
	if event == nil {
		return nil, nil
	}
	deletes := make([]*gnmipb.Path, 0, event.deleted.Size())
	dpaths := event.deleted.FindByPrefix(path)
	for _, dpath := range dpaths {
		if deleteOnly {
			if _, ok := event.updated.Find(dpath); ok {
				continue
			}
		}
		deleted := dpath[len(path):]
		datapath, err := gyangtree.ToGNMIPath(deleted)
		if err != nil {
			return nil, status.TaggedErrorf(codes.Internal, status.TagInvalidPath,
				"path converting error for %s", dpath)
		}
		deletes = append(deletes, datapath)
	}
	return deletes, nil
}

// getUpdates() returns updates with duplicates.
func getUpdates(branch, data yangtree.DataNode, encoding gnmipb.Encoding, event *changeEvent) (*gnmipb.Update, error) {
	// FIXME - need to check an empty notification is valid.
	// if ydb.IsEmptyInterface(data.Value) {
	// 	return nil, nil
	// }
	typedValue, err := gyangtree.DataNodeToTypedValue(data, encoding)
	if err != nil {
		return nil, status.TaggedErrorf(codes.Internal, status.TagBadData,
			"typed-value encoding error in %s: %v", data.Path(), err)
	}
	if typedValue == nil {
		return nil, nil
	}
	path := branch.PathTo(data)
	datapath, err := gyangtree.ToGNMIPath(path)
	if err != nil {
		return nil, status.TaggedErrorf(codes.Internal, status.TagInvalidPath,
			"path converting error for %s", path)
	}
	var duplicates uint32
	if event != nil {
		for _, v := range event.updated.FindByPrefixValue(path) {
			duplicates += v.(uint32)
		}
	}
	return &gnmipb.Update{Path: datapath, Val: typedValue, Duplicates: duplicates}, nil
}

func (subses *SubSession) clientAliasesUpdate(aliaslist *gnmipb.AliasList) error {
	aliasnames, err := subses.caliases.updateClientAliases(aliaslist.GetAlias())
	for _, name := range aliasnames {
		subses.respchan.Send(
			buildAliasResponse(subses.caliases.ToPath(name, true).(*gnmipb.Path), name))
	}
	return err
}

func (subses *SubSession) serverAliasesUpdate() {
	aliases := subses.caliases.updateServerAliases(subses.serverAliases, true)
	sort.Slice(aliases, func(i, j int) bool {
		return aliases[i] < aliases[j]
	})
	for _, alias := range aliases {
		subses.respchan.Send(
			buildAliasResponse(subses.caliases.ToPath(alias, true).(*gnmipb.Path), alias))
	}
}

// initTelemetryUpdate - Process and generate responses for a init update.
func (subses *SubSession) initTelemetryUpdate(
	gprefix *gnmipb.Path, sprefix string, spath []string,
	updatesOnly bool, encoding gnmipb.Encoding, event *changeEvent) error {
	if updatesOnly {
		return subses.respchan.Send(buildSyncResponse())
	}

	subses.RLock()
	defer subses.RUnlock()

	branches, err := yangtree.Find(subses.Root, sprefix)
	if err != nil || len(branches) <= 0 {
		if deletes, err := getDeletes("/", false, event); err != nil {
			return err
		} else if len(deletes) > 0 {
			prefixAlias := subses.caliases.ToAlias(gprefix, false).(*gnmipb.Path)
			err = subses.respchan.Send(
				buildSubscribeResponse(prefixAlias, nil, deletes))
			if err != nil {
				return err
			}
		}
		return subses.respchan.Send(buildSyncResponse())
	}

	for _, branch := range branches {
		bpath := branch.Path()
		deletes, err := getDeletes(bpath, false, event)
		if err != nil {
			return err
		}
		updates := make([]*gnmipb.Update, 0, len(spath))
		for i := range spath {
			nodes, err := yangtree.Find(branch, spath[i])
			if err != nil || len(nodes) <= 0 {
				continue
			}
			for _, node := range nodes {
				u, err := getUpdates(branch, node, encoding, event)
				if err != nil {
					return err
				}
				if u != nil {
					updates = append(updates, u)
				}
			}
		}
		if len(updates) > 0 || len(deletes) > 0 {
			prefixAlias := subses.caliases.ToAlias(gprefix, false).(*gnmipb.Path)
			err = subses.respchan.Send(
				buildSubscribeResponse(prefixAlias, updates, deletes))
			if err != nil {
				return err
			}
		}
	}
	return subses.respchan.Send(buildSyncResponse())
}

// telemetryUpdate - Process and generate responses for a telemetry update.
func (subses *SubSession) telemetryUpdate(sub *Subscriber, event *changeEvent) error {
	prefix := sub.Prefix
	encoding := sub.Encoding
	mode := sub.Mode
	streamMode := sub.StreamConfig.StreamMode
	root := subses.Root
	switch mode {
	case gnmipb.SubscriptionList_STREAM:
		root = subses.Root
		switch streamMode {
		case gnmipb.SubscriptionMode_ON_CHANGE:
			if event != nil {
				root = event.changes
			}
		}
	}

	subses.RLock()
	defer subses.RUnlock()
	branches, err := yangtree.Find(root, prefix)
	if err != nil || len(branches) <= 0 {
		if deletes, err := getDeletes("/", false, event); err != nil {
			return err
		} else if len(deletes) > 0 {
			prefixAlias := subses.caliases.ToAlias(sub.GPrefix, false).(*gnmipb.Path)
			err = subses.respchan.Send(
				buildSubscribeResponse(prefixAlias, nil, deletes))
			if err != nil {
				return err
			}
		}
		// data-missing is not an error in SubscribeRPC
		// does not send any of messages.
		return nil
	}

	for _, branch := range branches {
		var err error
		var deletes []*gnmipb.Path
		var updates []*gnmipb.Update
		bpath := branch.Path()
		// get all replaced, deleted paths relative to the prefix
		deletes, err = getDeletes(bpath, false, event)
		if err != nil {
			return err
		}
		updates = make([]*gnmipb.Update, 0, len(sub.Path))
		for i := range sub.Path {
			nodes, err := yangtree.Find(branch, sub.Path[i])
			if err != nil || len(nodes) <= 0 {
				continue
			}
			for _, data := range nodes {
				u, err := getUpdates(branch, data, encoding, event)
				if err != nil {
					return err
				}
				if u != nil {
					updates = append(updates, u)
				}
			}
		}
		if len(updates) > 0 || len(deletes) > 0 {
			prefixAlias := subses.caliases.ToAlias(sub.GPrefix, false).(*gnmipb.Path)
			err = subses.respchan.Send(
				buildSubscribeResponse(prefixAlias, updates, deletes))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	defaultInterval = 60000000000
	minimumInterval = 1000000000
)

// addSubscription adds a stream subscription to the subscription session.
func (subses *SubSession) addSubscription(name string, gprefix *gnmipb.Path, sprefix, spath string,
	useAliases bool, Mode gnmipb.SubscriptionList_Mode, allowAggregation bool,
	Encoding gnmipb.Encoding, StreamMode gnmipb.SubscriptionMode,
	SampleInterval uint64, SuppressRedundant bool, HeartbeatInterval uint64) (*Subscriber, error) {
	var key string
	if glog.V(11) {
		glog.Infof("subscribe[%s:%d:%d].%v",
			subses.Address, subses.Port, subses.ID, Mode)
	}
	if name != "" {
		key = name
	} else {
		switch Mode {
		case gnmipb.SubscriptionList_ONCE:
			return nil, nil
		case gnmipb.SubscriptionList_POLL:
			key = fmt.Sprintf("%d-%s-%s-%s-%t-%t",
				subses.ID, Mode, Encoding, sprefix,
				useAliases, allowAggregation)
		case gnmipb.SubscriptionList_STREAM:
			key = fmt.Sprintf("%d-%s-%s-%s-%s-%d-%d-%t-%t-%t",
				subses.ID, Mode, Encoding, StreamMode, sprefix,
				SampleInterval, HeartbeatInterval, useAliases,
				allowAggregation, SuppressRedundant)
		}
	}

	if glog.V(11) {
		glog.Infof("subscribe[%s:%d:%d].key=%v",
			subses.Address, subses.Port, subses.ID, key)
	}

	if subscriber, ok := subses.Sub[key]; ok {
		// only updates the new path if the sub exists.
		subscriber.mutex.Lock()
		defer subscriber.mutex.Unlock()
		for i := range subscriber.Path {
			if subscriber.Path[i] == spath {
				if glog.V(11) {
					glog.Infof("subscribe[%s:%d:%d].stream[%d]:: already added path: %s",
						subses.Address, subses.Port, subses.ID, subscriber.ID, spath)
				}
				return subscriber, nil
			}
		}
		subscriber.Path = append(subscriber.Path, spath)
		if glog.V(11) {
			glog.Infof("subscribe[%s:%d:%d].stream[%d]:: added path: %s",
				subses.Address, subses.Port, subses.ID, subscriber.ID, spath)
		}
		return subscriber, nil
	}
	subID++
	subscriber := &Subscriber{
		ID:                subID,
		SessionID:         subses.ID,
		Prefix:            sprefix,
		Path:              []string{spath},
		UseAliases:        useAliases,
		Mode:              Mode,
		AllowAggregation:  allowAggregation,
		Encoding:          Encoding,
		StreamMode:        StreamMode,
		SampleInterval:    SampleInterval,
		SuppressRedundant: SuppressRedundant,
		HeartbeatInterval: HeartbeatInterval,

		GPrefix:  gprefix,
		onchange: make(chan *changeEvent, 16),
		mutex:    &sync.Mutex{},
		session:  subses,
		key:      key,
	}
	subscriber.fullpath = append(subscriber.fullpath,
		subses.generateSyncPaths(sprefix, []string{spath})...)
	if Mode == gnmipb.SubscriptionList_POLL {
		if glog.V(11) {
			glog.Infof("subscribe[%s:%d:%d].stream[%d]:: added subscription",
				subses.Address, subses.Port, subses.ID, subscriber.ID)
			glog.Infof("subscribe[%s:%d:%d].stream[%d]:: added path: %s",
				subses.Address, subses.Port, subses.ID, subscriber.ID, spath)
		}
		subses.Sub[key] = subscriber
		return subscriber, nil
	}
	// 3.5.1.5.2 STREAM Subscriptions Must be satisfied for telemetry update starting.
	switch subscriber.StreamMode {
	case gnmipb.SubscriptionMode_TARGET_DEFINED:
		// vendor specific mode
		subscriber.StreamConfig.StreamMode = gnmipb.SubscriptionMode_SAMPLE
		subscriber.StreamConfig.SampleInterval = defaultInterval
		subscriber.StreamConfig.SuppressRedundant = true
		subscriber.StreamConfig.HeartbeatInterval = 0
	case gnmipb.SubscriptionMode_ON_CHANGE:
		if subscriber.HeartbeatInterval < minimumInterval && subscriber.HeartbeatInterval != 0 {
			return nil, status.TaggedErrorf(codes.OutOfRange, status.TagInvalidConfig,
				"heartbeat_interval(!= 0sec and < 1sec) is not supported")
		}
		if subscriber.SampleInterval != 0 {
			return nil, status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidConfig,
				"sample_interval not allowed on on_change mode")
		}
		subscriber.StreamConfig.StreamMode = gnmipb.SubscriptionMode_ON_CHANGE
		subscriber.StreamConfig.SampleInterval = 0
		subscriber.StreamConfig.SuppressRedundant = false
		subscriber.StreamConfig.HeartbeatInterval = subscriber.HeartbeatInterval
	case gnmipb.SubscriptionMode_SAMPLE:
		if subscriber.SampleInterval < minimumInterval && subscriber.SampleInterval != 0 {
			return nil, status.TaggedErrorf(codes.OutOfRange, status.TagInvalidConfig,
				"sample_interval(!= 0sec and < 1sec) is not supported")
		}
		if subscriber.HeartbeatInterval != 0 {
			if subscriber.HeartbeatInterval < minimumInterval {
				return nil, status.TaggedErrorf(codes.OutOfRange, status.TagInvalidConfig,
					"heartbeat_interval(!= 0sec and < 1sec) is not supported")
			}
			if subscriber.SampleInterval > subscriber.HeartbeatInterval {
				return nil, status.TaggedErrorf(codes.OutOfRange, status.TagInvalidConfig,
					"heartbeat_interval should be larger than sample_interval")
			}
		}

		subscriber.StreamConfig.StreamMode = gnmipb.SubscriptionMode_SAMPLE
		subscriber.StreamConfig.SampleInterval = subscriber.SampleInterval
		if subscriber.SampleInterval == 0 {
			// Set minimal sampling interval (1sec)
			subscriber.StreamConfig.SampleInterval = minimumInterval
		}
		subscriber.StreamConfig.SuppressRedundant = subscriber.SuppressRedundant
		subscriber.StreamConfig.HeartbeatInterval = subscriber.HeartbeatInterval
	}
	if glog.V(11) {
		glog.Infof("subscribe[%s:%d:%d].stream[%d]:: added subscription",
			subses.Address, subses.Port, subses.ID, subscriber.ID)
		glog.Infof("subscribe[%s:%d:%d].stream[%d]:: added path: %s",
			subses.Address, subses.Port, subses.ID, subscriber.ID, spath)
	}
	subses.Sub[key] = subscriber
	return subscriber, nil
}

// deleteSubscription deletes the stream subscription.
func (subses *SubSession) deleteSubscription(subscriber *Subscriber) {
	subscriber.Path = nil
	close(subscriber.onchange)
	subscriber.onchange = nil
	subscriber.session = nil
	subscriber.mutex = nil
	if glog.V(11) {
		glog.Infof("subscribe[%s:%d:%d].stream[%d]:: deleted subscription",
			subses.Address, subses.Port, subses.ID, subscriber.ID)
	}
}

func (subses *SubSession) processSubscribeRequest(req *gnmipb.SubscribeRequest) error {
	// SubscribeRequest for poll Subscription indication
	if pollMode := req.GetPoll(); pollMode != nil {
		for _, subscriber := range subses.Sub {
			if subscriber.Mode != gnmipb.SubscriptionList_POLL {
				continue
			}
			subses.SyncRequest(subscriber.fullpath)
			eventPending := subscriber.pending
			if err := subses.initTelemetryUpdate(
				subscriber.GPrefix, subscriber.Prefix, subscriber.Path,
				false, subscriber.Encoding, eventPending); err != nil {
				if glog.V(11) {
					glog.Errorf("subses[%d].poll[%d] %v", subses.ID, subscriber.ID, err)
				}
			}
			eventPending.Clear()
		}
		return nil
	}
	// SubscribeRequest for aliases update
	aliases := req.GetAliases()
	if aliases != nil {
		return subses.clientAliasesUpdate(aliases)
	}

	// extension := req.GetExtension()
	subscriptionList := req.GetSubscribe()
	if subscriptionList == nil {
		return status.TaggedErrorf(codes.InvalidArgument, status.TagMalformedMessage,
			"no subscription info (SubscriptionList field)")
	}
	// check & update the server aliases if use_aliases is true
	useAliases := subscriptionList.GetUseAliases()
	if useAliases {
		subses.serverAliasesUpdate()
	}

	subList := subscriptionList.GetSubscription()
	subListLength := len(subList)
	if subList == nil || subListLength <= 0 {
		return status.TaggedErrorf(codes.InvalidArgument, status.TagMalformedMessage,
			"no subscription info (subscription field)")
	}
	encoding := subscriptionList.GetEncoding()
	useModules := subscriptionList.GetUseModels()

	if err := subses.CheckModels(useModules); err != nil {
		return err
	}
	if err := subses.CheckEncoding(encoding); err != nil {
		return err
	}
	mode := subscriptionList.GetMode()
	oprefix := subscriptionList.GetPrefix()
	// convert alias to gnmi path
	gprefix := subses.caliases.ToPath(oprefix, false).(*gnmipb.Path)
	updatesOnly := subscriptionList.GetUpdatesOnly()

	gpath := make([]*gnmipb.Path, 0, len(subList))
	for _, sub := range subList {
		gpath = append(gpath, sub.Path)
	}
	sprefix, spath, err :=
		gyangtree.ValidateAndConvertGNMIPath(subses.RootSchema, gprefix, gpath)
	if err != nil {
		return status.TaggedError(codes.InvalidArgument, status.TagInvalidPath, err)
	}

	switch mode {
	case gnmipb.SubscriptionList_ONCE:
		subses.SyncRequest(subses.generateSyncPaths(sprefix, spath))
		return subses.initTelemetryUpdate(oprefix, sprefix, spath, updatesOnly, encoding, nil)
	case gnmipb.SubscriptionList_POLL, gnmipb.SubscriptionList_STREAM:
		allowAggregation := subscriptionList.GetAllowAggregation()
		startingList := make([]*Subscriber, 0, subListLength)
		for i := range subList {
			submod := subList[i].GetMode()
			SampleInterval := subList[i].GetSampleInterval()
			supressRedundant := subList[i].GetSuppressRedundant()
			heartBeatInterval := subList[i].GetHeartbeatInterval()
			subscriber, err := subses.addSubscription("",
				oprefix, sprefix, spath[i],
				useAliases, mode, allowAggregation,
				encoding, submod, SampleInterval,
				supressRedundant, heartBeatInterval)
			if err != nil {
				return err
			}
			startingList = append(startingList, subscriber)
		}
		for _, subscriber := range startingList {
			subses.addDynamicSubscription(subscriber)
			subses.Event.Register(subscriber)
			switch subscriber.Mode {
			case gnmipb.SubscriptionList_POLL:
			case gnmipb.SubscriptionList_STREAM:
				subses.SyncRequest(subscriber.fullpath)
				if err := subses.initTelemetryUpdate(
					subscriber.GPrefix, subscriber.Prefix, subscriber.Path,
					updatesOnly, subscriber.Encoding, nil); err != nil {
					return err
				}
				if !subscriber.started {
					subscriber.started = true
					subses.waitgroup.Add(1)
					go subscriber.run()
				}
			}
		}
	}
	return nil
}
