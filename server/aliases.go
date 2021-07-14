package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/glog"
	"github.com/neoul/open-gnmi/utilities/status"
	gyangtree "github.com/neoul/yangtree/gnmi"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
	"github.com/openconfig/goyang/pkg/yang"
	"google.golang.org/grpc/codes"
)

type aliasEntry struct {
	Name     string       // alias name
	Path     string       // string path
	GNMIPath *gnmipb.Path // gnmi path
	IsServer bool         // true if target-defined aliases
}

func (entry *aliasEntry) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`{"name": %q, "path": %q, "gpath": %q, "is-server": %v}`, entry.Name, entry.Path, entry.GNMIPath, entry.IsServer)), nil
}

// Client Aliases structure provides the conversion of the gnmi aliases
type clientAliases struct {
	Alias2Path       map[string]*aliasEntry `json:"alias-to-path,omitempty"`
	Path2Alias       map[string]*aliasEntry `json:"path-to-alias,omitempty"`
	UseServerAliases bool                   `json:"use-server-aliases,omitempty"`

	schema *yang.Entry
	mutex  *sync.RWMutex
}

func (caliases *clientAliases) String() string {
	jbytes, err := json.Marshal(caliases)
	if err != nil {
		return err.Error()
	}
	return string(jbytes)
}

// clientAliases is initialized with server aliases (target-defined aliases)
func newClientAliases(schema *yang.Entry) *clientAliases {
	if schema == nil {
		return nil
	}
	caliases := &clientAliases{
		Alias2Path: make(map[string]*aliasEntry),
		Path2Alias: make(map[string]*aliasEntry),
		schema:     schema,
		mutex:      &sync.RWMutex{},
	}
	return caliases
}

func clearClientAliases(caliases *clientAliases) {
	mutex := caliases.mutex
	mutex.Lock()
	defer mutex.Unlock()
	caliases.mutex = nil
	for k, v := range caliases.Alias2Path {
		v.GNMIPath = nil
		delete(caliases.Alias2Path, k)
	}
	for k, v := range caliases.Path2Alias {
		v.GNMIPath = nil
		delete(caliases.Path2Alias, k)
	}
}

// update updates or deletes the server aliases and returns the updated aliases.
func (caliases *clientAliases) updateServerAliases(serverAliases map[string]string, add bool) []string {
	if caliases == nil {
		return nil
	}
	caliases.mutex.Lock()
	defer caliases.mutex.Unlock()
	aliaslist := make([]string, 0, len(serverAliases))
	for name, path := range serverAliases {
		if !strings.HasPrefix(name, "#") {
			if glog.V(11) {
				glog.Errorf("invalid server alias %q found", name)
			}
			continue
		}
		gpath, err := gyangtree.ToGNMIPath(path)
		if err != nil {
			if glog.V(11) {
				glog.Errorf("invalid server alias path: %v", err)
			}
			continue
		}
		if add {
			if _, ok := caliases.Alias2Path[name]; !ok {
				ca := &aliasEntry{
					Name:     name,
					Path:     path,
					GNMIPath: gpath,
					IsServer: true,
				}
				caliases.Path2Alias[path] = ca
				caliases.Alias2Path[name] = ca
				aliaslist = append(aliaslist, name)
			}
		} else {
			if ca, ok := caliases.Alias2Path[name]; ok && ca.IsServer {
				delete(caliases.Alias2Path, name)
				delete(caliases.Path2Alias, ca.Path)
				aliaslist = append(aliaslist, name)
			}
		}
	}
	caliases.UseServerAliases = add
	return aliaslist
}

// Set sets the client alias to the clientAliases structure.
func (caliases *clientAliases) updateClientAlias(alias *gnmipb.Alias) error {
	if caliases == nil {
		return status.TaggedErrorf(codes.Internal, status.TagOperationFail, "nil client-aliases")
	}
	caliases.mutex.Lock()
	defer caliases.mutex.Unlock()
	name := alias.GetAlias()
	if name == "" {
		return status.TaggedErrorf(codes.InvalidArgument,
			status.TagInvalidAlias, "empty alias")
	}
	if !strings.HasPrefix(name, "#") {
		return status.TaggedErrorf(codes.InvalidArgument,
			status.TagInvalidAlias, "alias must start with '#'. e.g. %s", name)
	}
	gpath := alias.GetPath()
	if gpath == nil || len(gpath.GetElem()) == 0 {
		// delete the alias
		if ca, ok := caliases.Alias2Path[name]; ok {
			delete(caliases.Alias2Path, name)
			delete(caliases.Path2Alias, ca.Path)
		}
		return nil
	}

	if gyangtree.HasGNMIPathWildcards(gpath) {
		return status.TaggedErrorf(codes.InvalidArgument,
			status.TagInvalidAlias, "invalid alias path: wildcard not allowed for the alias")
	}

	path, err := gyangtree.ToValidDataPath(caliases.schema, gpath)
	if err != nil {
		return status.TaggedErrorf(codes.InvalidArgument,
			status.TagInvalidAlias, "invalid alias path: %v", err)
	}

	if caliases.schema != nil {
		if err := gyangtree.ValidateGNMIPath(caliases.schema, gpath); err != nil {
			return status.TaggedErrorf(codes.InvalidArgument, status.TagInvalidAlias,
				"invalid path '%s'", path)
		}
	}
	if ca, ok := caliases.Path2Alias[path]; ok {
		return status.TaggedErrorf(codes.AlreadyExists, status.TagInvalidAlias,
			"'%s'is already defined for '%s'", ca.Name, path)
	}
	if ca, ok := caliases.Alias2Path[name]; ok {
		return status.TaggedErrorf(codes.AlreadyExists, status.TagInvalidAlias,
			"'%s' is already defined for '%s'", name, ca.Path)
	}
	// add the alias
	ca := &aliasEntry{
		Name:     name,
		Path:     path,
		GNMIPath: gpath,
	}
	caliases.Path2Alias[path] = ca
	caliases.Alias2Path[name] = ca
	return nil
}

func (caliases *clientAliases) updateClientAliases(aliases []*gnmipb.Alias) ([]string, error) {
	if caliases == nil {
		return nil, status.TaggedErrorf(codes.Internal, status.TagOperationFail, "nil client-aliases")
	}
	var aliasname []string
	for _, alias := range aliases {
		if err := caliases.updateClientAlias(alias); err != nil {
			return aliasname, err
		}
		aliasname = append(aliasname, alias.GetAlias())
		if glog.V(11) {
			glog.Infof("set alias %q to %q", alias.GetAlias(), alias.GetPath())
		}
	}
	return aliasname, nil
}

// ToPath converts an alias to the related path.
// If the input alias is a string, it will return a string path.
// If the input alias is a gnmipb.Path, it will return the same type path.
// if diffFormat is configured, it will return the different type.
//  [gNMI path --> string path, string path --> gNMI path]
func (caliases *clientAliases) ToPath(alias interface{}, diffFormat bool) interface{} {
	if caliases == nil {
		return alias
	}
	caliases.mutex.RLock()
	defer caliases.mutex.RUnlock()
	switch a := alias.(type) {
	case *gnmipb.Path:
		if a == nil {
			if diffFormat {
				return ""
			}
			return a
		}
		if len(a.Elem) > 0 {
			if strings.HasPrefix(a.Elem[0].GetName(), "#") {
				if ca, ok := caliases.Alias2Path[a.Elem[0].Name]; ok {
					if diffFormat {
						return ca.Path
					}
					return replacePathElem(a, ca.GNMIPath)
				}
			}
		}
		if diffFormat {
			path, _ := gyangtree.ToValidDataPath(caliases.schema, a)
			return path
		}
		return a
	case string:
		if ca, ok := caliases.Alias2Path[a]; ok {
			if diffFormat {
				return ca.GNMIPath
			}
			return ca.Path
		}
		if diffFormat {
			o, _ := gyangtree.ToGNMIPath(a)
			return o
		}
		return a
	}
	// must not reach here!!!
	glog.Fatalf("unknown type inserted to clientAliases.ToPath()")
	return alias
}

// ToAlias converts a path to the related alias.
func (caliases *clientAliases) ToAlias(path interface{}, diffFormat bool) interface{} {
	if caliases == nil {
		return path
	}
	caliases.mutex.RLock()
	defer caliases.mutex.RUnlock()
	switch _path := path.(type) {
	case *gnmipb.Path:
		p, _ := gyangtree.ToValidDataPath(caliases.schema, _path)
		if ca, ok := caliases.Path2Alias[p]; ok {
			if diffFormat {
				return ca.Name
			}
			return newGNMIAliasPath(ca.Name, _path.Target, _path.Origin)
		}
		if diffFormat {
			return p
		}
		return _path
	case string:
		if ca, ok := caliases.Path2Alias[_path]; ok {
			if diffFormat {
				return newGNMIAliasPath(ca.Name, "", "")
			}
			return ca.Name
		}
		if diffFormat {
			o, _ := gyangtree.ToGNMIPath(_path)
			return o
		}
		return _path
	default:
	}
	// must not reach here!!!
	glog.Fatalf("unknown type inserted to clientAliases.ToAlias()")
	return path
}

// replacePathElem update the dest path using the src path.
func replacePathElem(dest, src *gnmipb.Path) *gnmipb.Path {
	if dest == nil {
		return proto.Clone(src).(*gnmipb.Path)
	}
	if src == nil {
		return dest
	}
	dest.Elem = make([]*gnmipb.PathElem, 0, len(src.Elem))
	for i := range src.Elem {
		if src.Elem[i] != nil {
			elem := &gnmipb.PathElem{Name: src.Elem[i].Name}
			if len(src.Elem[i].Key) > 0 {
				elem.Key = make(map[string]string)
				for k, v := range src.Elem[i].Key {
					elem.Key[k] = v
				}
			}
			dest.Elem = append(dest.Elem, elem)
		}
	}
	return dest
}

// newGNMIAliasPath returns Alias gNMI Path.
func newGNMIAliasPath(name, target, origin string) *gnmipb.Path {
	return &gnmipb.Path{
		Target: target,
		Origin: origin,
		Elem: []*gnmipb.PathElem{
			&gnmipb.PathElem{
				Name: name,
			},
		},
	}
}
