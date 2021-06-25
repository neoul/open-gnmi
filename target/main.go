// package main for gnmi target (gnmi server)
package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"

	"flag"

	"github.com/golang/glog"
	"github.com/neoul/open-gnmi/server"
	"github.com/neoul/open-gnmi/utilities/server/credentials"
	"github.com/neoul/yangtree"
	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// go:generate sh -c "go get -u github.com/openconfig/public; go get github.com/openconfig/public"

var (
	bindAddr = pflag.StringP("bind-address", "b", ":57400", "bind to address:port")
	startup  = pflag.String("startup", "", "startup data formatted to ietf-json or yaml")
	// startupFormat = pflag.String("startup-format", "", "startup data format [ietf-json, json, yaml], default: ietf-json")
	help       = pflag.BoolP("help", "h", false, "help for gnmid")
	ca         = pflag.String("ca-crt", "", "ca certificate file")
	crt        = pflag.String("server-crt", "", "server certificate file")
	key        = pflag.String("server-key", "", "server private key file")
	skipVerify = pflag.Bool("skip-verify", false, "skip tls connection verfication")
	insecure   = pflag.Bool("insecure", false, "disable tls (transport layer security) to run grpc insecure mode")
	yang       = pflag.StringArray("yang", []string{}, "yang files to be loaded")
	dir        = pflag.StringArray("dir", []string{}, "directories to search yang includes and imports")
	excludes   = pflag.StringArray("exclude", []string{}, "yang modules to be excluded from path generation")
	pathPrint  = pflag.Bool("path-print", false, "path printing")
)

func yangfiles() ([]string, []string, []string) {
	files := []string{
		"../../../YangModels/yang/standard/ietf/RFC/iana-if-type@2017-01-19.yang",
		"../../../openconfig/public/release/models/interfaces/openconfig-interfaces.yang",
		"../../../openconfig/public/release/models/interfaces/openconfig-if-ip.yang",
		"../../../openconfig/public/release/models/system/openconfig-messages.yang",
		"../../../openconfig/public/release/models/telemetry/openconfig-telemetry.yang",
		"../../../openconfig/public/release/models/openflow/openconfig-openflow.yang",
		"../../../openconfig/public/release/models/platform/openconfig-platform.yang",
		"../../../openconfig/public/release/models/system/openconfig-system.yang",
		"../../../neoul/yangtree/data/sample/sample.yang",
	}
	dir := []string{"../../../openconfig/public/", "../../../YangModels/yang"}
	excluded := []string{"ietf-interfaces"}
	return files, dir, excluded
}

func main() {
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	if *help {
		fmt.Fprintf(pflag.CommandLine.Output(), "\n gnmi target (grpc network management interface server)\n")
		fmt.Fprintf(pflag.CommandLine.Output(), "\n")
		fmt.Fprintf(pflag.CommandLine.Output(), " Usage: %s [Flag]\n", os.Args[0])
		fmt.Fprintf(pflag.CommandLine.Output(), "\n")
		pflag.PrintDefaults()
		fmt.Fprintf(pflag.CommandLine.Output(), "\n")
		os.Exit(1)
	}

	opts, err := credentials.ServerCredentials(*ca, *crt, *key, *skipVerify, *insecure)
	if err != nil {
		glog.Exitf("server credential loading failed: %v", err)
	}
	if len(*yang) == 0 {
		f, d, e := yangfiles()
		yang, dir, excludes = &f, &d, &e
	}
	gnmiserver, err := server.NewServer(*yang, *dir, *excludes)
	if err != nil {
		glog.Exitf("gnmi new server failed: %v", err)
	}
	if *pathPrint {
		ss := yangtree.CollectSchemaEntries(gnmiserver.RootSchema, true)
		for i := range ss {
			fmt.Println(yangtree.GeneratePath(ss[i], false, false))
		}
		os.Exit(0)
	}

	if *startup != "" {
		loadbytes, err := ioutil.ReadFile(*startup)
		if err != nil {
			glog.Exitf("error in reading startup file: %v", err)
		}
		err = gnmiserver.Load(loadbytes, server.Encoding_JSON)
		if err != nil {
			glog.Exitf("error in loading startup: %v", err)
		}
	}

	err = Subsystem(gnmiserver)
	if err != nil {
		glog.Exitf("error in subsystem loading: %v", err)
	}

	// opts = append(opts, grpc.UnaryInterceptor(login.UnaryInterceptor))
	// opts = append(opts, grpc.StreamInterceptor(login.StreamInterceptor))
	grpcserver := grpc.NewServer(opts...)
	gnmipb.RegisterGNMIServer(grpcserver, gnmiserver)
	reflection.Register(grpcserver)
	listen, err := net.Listen("tcp", *bindAddr)
	if err != nil {
		glog.Exitf("listen failed: %v", err)
	}
	if err := grpcserver.Serve(listen); err != nil {
		grpcserver.Stop()
		glog.Exitf("serve failed: %v", err)
	}
}
