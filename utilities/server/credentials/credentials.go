package credentials

// Package credentials loads certificates for server

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// LoadCA loads Root CA from file.
func LoadCAFromFile(cafile string) (*x509.CertPool, error) {
	var certPool *x509.CertPool
	// Server runs without certpool if cafile is not configured.
	// creds, err := credentials.NewServerTLSFromFile(certfile, keyfile)
	if cafile != "" {
		certPool = x509.NewCertPool()
		cabytes, err := ioutil.ReadFile(cafile)
		if err != nil {
			return nil, err
		}
		if ok := certPool.AppendCertsFromPEM(cabytes); !ok {
			return nil, errors.New("failed to append ca certificate")
		}
	}
	return certPool, nil
}

func LoadCA(ca []byte) (*x509.CertPool, error) {
	var certPool *x509.CertPool
	// Server runs without certpool if cafile is not configured.
	// creds, err := credentials.NewServerTLSFromFile(certfile, keyfile)
	if len(ca) > 0 {
		certPool = x509.NewCertPool()
		if ok := certPool.AppendCertsFromPEM([]byte(ca)); !ok {
			return nil, errors.New("failed to append ca certificate")
		}
	}
	return certPool, nil
}

// LoadCertificates loads certificates from file.
func LoadCertificatesFromFile(certfile, keyfile string) ([]tls.Certificate, error) {
	if certfile == "" && keyfile == "" {
		return []tls.Certificate{}, nil
	}
	certificate, err := tls.LoadX509KeyPair(certfile, keyfile)
	if err != nil {
		return nil, err
	}
	return []tls.Certificate{certificate}, nil
}

// LoadCertificates loads certificates from file.
func LoadCertificates(cert, key []byte) ([]tls.Certificate, error) {
	if len(cert) == 0 && len(key) == 0 {
		return []tls.Certificate{}, nil
	}
	certificate, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	return []tls.Certificate{certificate}, nil
}

func ServerCredentials(cafile, certfile, keyfile string, skipVerifyTLS, insecure bool) ([]grpc.ServerOption, error) {
	if insecure {
		return nil, nil
	}
	certPool, err := LoadCAFromFile(cafile)
	if err != nil {
		return nil, fmt.Errorf("ca loading failed: %v", err)
	}

	certificates, err := LoadCertificatesFromFile(certfile, keyfile)
	if err != nil {
		return nil, fmt.Errorf("server certificates loading failed: %v", err)
	}
	if skipVerifyTLS {
		return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(&tls.Config{
			ClientAuth:   tls.VerifyClientCertIfGiven,
			Certificates: certificates,
			ClientCAs:    certPool,
		}))}, nil
	}
	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(&tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		Certificates: certificates,
		ClientCAs:    certPool,
	}))}, nil
}
