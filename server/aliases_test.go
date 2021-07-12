package server

import (
	"reflect"
	"testing"

	"github.com/neoul/yangtree"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
)

func Test_clientAliases(t *testing.T) {
	serveraliases := map[string]string{
		"#1/1":        "/interfaces/interface[name=1/1]",
		"#1/2":        "/interfaces/interface[name=1/2]",
		"#1/3":        "/interfaces/interface[name=1/3]",
		"#1/4":        "/interfaces/interface[name=1/4]",
		"#1/5":        "/interfaces/interface[name=1/5]",
		"#eth0-stats": "/interfaces/interface[name=eth0]/state",
		"#log":        "/messages/state/msg",
	}
	schema, err := yangtree.Load(testModels())
	if err != nil {
		t.Fatalf("loading schema failed: %v", err)
	}
	caliases := newClientAliases(schema)

	// enable server aliases
	caliases.updateServerAliases(serveraliases, true)

	type setTest struct {
		name    string
		alias   *gnmipb.Alias
		wantErr bool
	}
	settests := []setTest{
		{
			name: "Set",
			alias: &gnmipb.Alias{
				Alias: "#mgmt",
				Path: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "interfaces",
						},
						&gnmipb.PathElem{
							Name: "interface",
							Key: map[string]string{
								"name": "mgmt",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Set",
			alias: &gnmipb.Alias{
				Alias: "#mysystem",
				Path: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "system",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Set",
			alias: &gnmipb.Alias{
				Alias: "#first-if",
				Path: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "interfaces",
						},
						&gnmipb.PathElem{
							Name: "interface",
							Key: map[string]string{
								"name": "1/1",
							},
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range settests {
		t.Run(tt.name, func(t *testing.T) {
			if err := caliases.updateClientAlias(tt.alias); err != nil && !tt.wantErr {
				t.Errorf("clientAliases.Set() = %v", err)
			}
		})
	}

	type args struct {
		input      interface{}
		diffFormat bool
	}
	type test struct {
		name string
		args args
		want interface{}
	}
	tests := []test{
		{
			name: "ToPath (string path --> string path)",
			args: args{
				input:      "#log",
				diffFormat: false,
			},
			want: "/messages/state/msg",
		},
		{
			name: "ToPath (string path --> gnmi path)",
			args: args{
				input:      "#eth0-stats",
				diffFormat: true,
			},
			want: &gnmipb.Path{
				Elem: []*gnmipb.PathElem{
					&gnmipb.PathElem{
						Name: "interfaces",
					},
					&gnmipb.PathElem{
						Name: "interface",
						Key: map[string]string{
							"name": "eth0",
						},
					},
					&gnmipb.PathElem{
						Name: "state",
					},
				},
			},
		},
		{
			name: "ToPath (gnmi path --> gnmi path)",
			args: args{
				input:      newGNMIAliasPath("#1/3", "", ""),
				diffFormat: false,
			},
			want: &gnmipb.Path{
				Elem: []*gnmipb.PathElem{
					&gnmipb.PathElem{
						Name: "interfaces",
					},
					&gnmipb.PathElem{
						Name: "interface",
						Key: map[string]string{
							"name": "1/3",
						},
					},
				},
			},
		},
		{
			name: "ToPath (gnmi path --> string path)",
			args: args{
				input:      newGNMIAliasPath("#1/3", "", ""),
				diffFormat: true,
			},
			want: "/interfaces/interface[name=1/3]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := caliases.ToPath(tt.args.input, tt.args.diffFormat); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("clientAliases.ToPath() = %v, want %v", got, tt.want)
			}
		})
	}
	tests = []test{
		{
			name: "ToAlias",
			args: args{
				input:      "/messages/state",
				diffFormat: false,
			},
			want: "/messages/state", // not changed because there is no matched alias.
		},
		{
			name: "ToAlias",
			args: args{
				input:      "/messages/state/msg",
				diffFormat: false,
			},
			want: "#log",
		},
		{
			name: "ToAlias",
			args: args{
				input:      "/interfaces/interface[name=1/3]",
				diffFormat: true,
			},
			want: newGNMIAliasPath("#1/3", "", ""),
		},
		{
			name: "ToAlias",
			args: args{
				input: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "interfaces",
						},
						&gnmipb.PathElem{
							Name: "interface",
							Key: map[string]string{
								"name": "eth0",
							},
						},
						&gnmipb.PathElem{
							Name: "state",
						},
					},
				},
				diffFormat: false,
			},
			want: newGNMIAliasPath("#eth0-stats", "", ""),
		},
		{
			name: "ToAlias",
			args: args{
				input: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "interfaces",
						},
						&gnmipb.PathElem{
							Name: "interface",
							Key: map[string]string{
								"name": "eth0",
							},
						},
						&gnmipb.PathElem{
							Name: "state",
						},
					},
				},
				diffFormat: true,
			},
			want: "#eth0-stats",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := caliases.ToAlias(tt.args.input, tt.args.diffFormat); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("clientAliases.ToAlias() = %v, want %v", got, tt.want)
			}
		})
	}

	// disable server aliases
	caliases.updateServerAliases(serveraliases, false)

	// server aliases are removed from client aliases.
	tests = []test{
		{
			name: "ToAlias",
			args: args{
				input:      "/messages/state/msg",
				diffFormat: false,
			},
			want: "/messages/state/msg",
		},
		{
			name: "ToAlias",
			args: args{
				input:      "/interfaces/interface[name=1/3]",
				diffFormat: false,
			},
			want: "/interfaces/interface[name=1/3]",
		},
		{
			name: "ToAlias",
			args: args{
				input: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "interfaces",
						},
						&gnmipb.PathElem{
							Name: "interface",
							Key: map[string]string{
								"name": "eth0",
							},
						},
						&gnmipb.PathElem{
							Name: "state",
						},
					},
				},
				diffFormat: true,
			},
			want: "/interfaces/interface[name=eth0]/state",
		},
		{
			name: "ToAlias",
			args: args{
				input: &gnmipb.Path{
					Elem: []*gnmipb.PathElem{
						&gnmipb.PathElem{
							Name: "system",
						},
					},
				},
				diffFormat: true,
			},
			want: "#mysystem",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := caliases.ToAlias(tt.args.input, tt.args.diffFormat); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("clientAliases.ToAlias() = %v, want %v", got, tt.want)
			}
		})
	}
}
