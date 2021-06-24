package server

import (
	"reflect"
	"testing"

	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
)

func Test_clientAliases(t *testing.T) {
	serveraliases := map[string]string{
		"#1/1":     "/interfaces/interface[name=1/1]",
		"#1/2":     "/interfaces/interface[name=1/2]",
		"#1/3":     "/interfaces/interface[name=1/3]",
		"#1/4":     "/interfaces/interface[name=1/4]",
		"#1/5":     "/interfaces/interface[name=1/5]",
		"#ifstate": "/interfaces/interface/state",
		"#log":     "/messages/state/msg",
	}
	caliases := newClientAliases(nil)

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
				t.Errorf("ClientAliases.Set() = %v", err)
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
				input:      "#ifstate",
				diffFormat: true,
			},
			want: &gnmipb.Path{
				Elem: []*gnmipb.PathElem{
					&gnmipb.PathElem{
						Name: "interfaces",
					},
					&gnmipb.PathElem{
						Name: "interface",
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
				t.Errorf("ClientAliases.ToPath() = %v, want %v", got, tt.want)
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
						},
						&gnmipb.PathElem{
							Name: "state",
						},
					},
				},
				diffFormat: false,
			},
			want: newGNMIAliasPath("#ifstate", "", ""),
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
						},
						&gnmipb.PathElem{
							Name: "state",
						},
					},
				},
				diffFormat: true,
			},
			want: "#ifstate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := caliases.ToAlias(tt.args.input, tt.args.diffFormat); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ClientAliases.ToAlias() = %v, want %v", got, tt.want)
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
						},
						&gnmipb.PathElem{
							Name: "state",
						},
					},
				},
				diffFormat: true,
			},
			want: "/interfaces/interface/state",
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
				t.Errorf("ClientAliases.ToAlias() = %v, want %v", got, tt.want)
			}
		})
	}
}
