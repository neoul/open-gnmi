package server

import (
	"flag"
	"testing"
	"time"

	"github.com/neoul/open-gnmi/utilities/test"
	"github.com/neoul/yangtree"
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
		sprefix string
		spaths  []string
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "SyncRequest1",
			args: args{
				spaths: []string{
					"interfaces",
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
			name: "SyncRequest2",
			args: args{
				sprefix: "/interfaces",
				spaths:  []string{"interface/config"},
			},
			want: []string{
				"/interfaces/interface[name=p1]/config/enabled",
				"/interfaces/interface[name=p2]/config/enabled",
			},
		},
		{
			name: "SyncRequest3",
			args: args{
				sprefix: "/interfaces",
				spaths:  []string{"interface[name=p1]/config"},
			},
			want: []string{
				"/interfaces/interface[name=p1]/config/enabled",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.SyncRequest(s.generateSyncPaths(tt.args.sprefix, tt.args.spaths))
			if !test.IsEqualList(tsync.syncpath, tt.want) {
				t.Errorf("SyncRequest() got = %v, want %v", tsync.syncpath, tt.want)
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
