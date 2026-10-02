//go:build !linux

package nfsexport

import (
	"errors"
	"log"
	"net"
)

func ActivatedListener() (net.Listener, error) {
	return nil, errors.New("the NFS helper runs only on Linux")
}

func Serve(listener net.Listener, policy Policy, logger *log.Logger) error {
	return errors.New("the NFS helper runs only on Linux")
}
