package server

import (
	"flag"
	"testing"
	"time"

	"github.com/neoul/open-gnmi/utilities/test"
	"github.com/neoul/yangtree"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
)

type testSync struct {
	syncpath []string
}

func (tsync *testSync) SyncCallback(syncPath ...string) error {
	tsync.syncpath = append(tsync.syncpath, syncPath...)
	return nil
}

func TestSync(t *testing.T) {
	// for debug
	if testing.Verbose() {
		flag.Set("alsologtostderr", "true")
		flag.Set("v", "100")
	}
	syncRequiredPath := []string{
		"/interfaces/interface/state/counters",
		"/interfaces/interface/state/enabled",
		"/interfaces/interface/config/enabled",
	}
	tsync := &testSync{}
	file, dir, excluded := testModels()
	s, err := NewServer(file, dir, excluded,
		SyncCallbackOption{SyncCallback: tsync, MinInterval: 1 * time.Nanosecond})
	if err != nil {
		t.Errorf("failed to create a model: %v", err)
	}
	if err := s.RegisterSync(syncRequiredPath...); err != nil {
		t.Errorf("fail to set sync-path: %v", err)
	}
	jstr := `{
		"openconfig-messages:messages": {
			"config": {
				"severity": "ERROR"
			},
			"state": {
				"severity": "ERROR",
				"message": {
					"msg" : "Messages presents here.",
					"priority": 10
				}
			}
		},
		"openconfig-interfaces:interfaces": {
			"interface": [
				{
					"name": "p1",
					"config": {
						"name": "p1",
						"type": "iana-if-type:ethernetCsmacd",
						"mtu": 1516,
						"loopback-mode": false,
						"description": "Interface#1",
						"enabled": true
					}
				},
				{
					"name": "p2",
					"config": {
						"name": "p2",
						"type": "iana-if-type:ethernetCsmacd",
						"mtu": 1516,
						"loopback-mode": false,
						"description": "n/a",
						"enabled": true
					},
					"state": {
						"counters": {}
					}
				}
			]
		}
	}`
	if err := s.Load([]byte(jstr), Encoding_JSON_IETF); err != nil {
		t.Fatalf("error in loading state: %v", err)
	}
	c, _, _ := yangtree.Diff(nil, s.Root)
	// set the load operation as an event to register sync data.
	s.Event.SetEvent(c, nil, nil)
	type args struct {
		prefix *gnmipb.Path
		paths  []*gnmipb.Path
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "syncRequest 1",
			args: args{
				paths: []*gnmipb.Path{
					&gnmipb.Path{
						Elem: []*gnmipb.PathElem{
							&gnmipb.PathElem{
								Name: "interfaces",
							},
						},
					},
				},
			},
			want: []string{
				"/interfaces/interface[name=p2]/config/enabled",
				"/interfaces/interface[name=p1]/config/enabled",
				"/interfaces/interface[name=p2]/state/counters",
				"/interfaces/interface[name=p2]/state/enabled",
			},
		},
		{
			name: "syncRequest 2",
			args: args{
				prefix: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "interfaces",
						},
					},
				},
				paths: []*gnmipb.Path{
					&gnmipb.Path{
						Elem: []*gnmipb.PathElem{
							&gnmipb.PathElem{
								Name: "interface",
							},
							&gnmipb.PathElem{
								Name: "config",
							},
						},
					},
				},
			},
			want: []string{
				"/interfaces/interface[name=p1]/config/enabled",
				"/interfaces/interface[name=p2]/config/enabled",
			},
		},
		{
			name: "syncRequest 3",
			args: args{
				prefix: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "interfaces",
						},
					},
				},
				paths: []*gnmipb.Path{
					&gnmipb.Path{
						Elem: []*gnmipb.PathElem{
							&gnmipb.PathElem{
								Name: "interface",
								Key: map[string]string{
									"name": "p1",
								},
							},
							&gnmipb.PathElem{
								Name: "config",
							},
						},
					},
				},
			},
			want: []string{
				"/interfaces/interface[name=p1]/config/enabled",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.syncRequest(tt.args.prefix, tt.args.paths)
			if !test.IsEqualList(tsync.syncpath, tt.want) {
				t.Errorf("syncRequest() got = %v, want %v", tsync.syncpath, tt.want)
				for _, g := range tsync.syncpath {
					t.Log("tsync.syncpath::", g)
				}
				for _, g := range tt.want {
					t.Log("tt.want::", g)
				}
			}
			tsync.syncpath = []string{}
		})
	}
}
